package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/logging"
)

// The git plumbing of a stack split (stack_split.go). Every command goes
// through gitexec; nothing is written to any process's stdin
// (TestNoWritesToProcessesItervoxDidNotStart).

// stackSplitTrailer is the trailer a split commit carries after the head
// commit's own message, saying which half it is. A message's earlier
// stack split trailers are dropped first, so splitting a split commit
// again (a resumed run) builds the same message.
const stackSplitTrailer = "Itervox-Stack-Split:"

// splitArgBudget bounds the bytes of the paths one update-index call
// takes as arguments: well under Windows' 32 KiB command line, and
// nowhere near the ARG_MAX of other systems.
const splitArgBudget = 24 << 10

// gitStderr is err with git's stderr appended (exec.ExitError keeps it
// when the command's Stderr was not set), redacted.
func gitStderr(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		return err.Error() + ": " + logging.RedactString(strings.TrimSpace(string(ee.Stderr)))
	}
	return err.Error()
}

// stackSplitAuthorEnv makes a split commit's author and committer those of
// the run's head commit, so the same split builds the same commits again
// (a resumed run pushes the lower branch it already pushed, unchanged).
func stackSplitAuthorEnv(ctx context.Context, ws, sha string) ([]string, error) {
	out, err := gitexec.Command(ctx, ws, "log", "-1", "--date=raw",
		"--format=%an%x00%ae%x00%ad%x00%cn%x00%ce%x00%cd", sha).Output()
	if err != nil {
		return nil, fmt.Errorf("orchestrator: read the head commit's author: %s", gitStderr(err))
	}
	f := strings.Split(strings.TrimRight(string(out), "\n"), "\x00")
	if len(f) != 6 {
		return nil, fmt.Errorf("orchestrator: unexpected author line for %s", shortHash(sha))
	}
	return []string{
		"GIT_AUTHOR_NAME=" + f[0], "GIT_AUTHOR_EMAIL=" + f[1], "GIT_AUTHOR_DATE=" + f[2],
		"GIT_COMMITTER_NAME=" + f[3], "GIT_COMMITTER_EMAIL=" + f[4], "GIT_COMMITTER_DATE=" + f[5],
	}, nil
}

// trailerLine is a "Token: value" line of a git trailer block.
var trailerLine = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*: `)

// stackSplitMessage is the head commit's full message (subject, body,
// trailers) with note added as a stack split trailer: at the end of the
// message's trailer block when it ends with one (Signed-off-by and the
// like stay trailers), else as a trailer block of its own.
func stackSplitMessage(headMsg, note string) string {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(headMsg, "\r\n", "\n"), "\n"), "\n")
	lines = slices.DeleteFunc(lines, func(l string) bool { return strings.HasPrefix(l, stackSplitTrailer) })
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	trailer := stackSplitTrailer + " " + note
	if len(lines) == 0 {
		return "Stack split\n\n" + trailer + "\n"
	}
	start := len(lines)
	for start > 1 && trailerLine.MatchString(lines[start-1]) {
		start--
	}
	endsWithTrailers := start < len(lines) && start > 1 && strings.TrimSpace(lines[start-1]) == ""
	if endsWithTrailers {
		return strings.Join(lines, "\n") + "\n" + trailer + "\n"
	}
	return strings.Join(lines, "\n") + "\n\n" + trailer + "\n"
}

// argBatches cuts args into batches whose bytes stay within budget (a
// single larger argument is a batch of its own). pairs keeps arguments
// that belong together (--cacheinfo and its value) in one batch.
func argBatches(args []string, pairs int, budget int) [][]string {
	var out [][]string
	var cur []string
	size := 0
	for i := 0; i < len(args); i += pairs {
		unit := args[i:min(i+pairs, len(args))]
		n := 0
		for _, a := range unit {
			n += len(a) + 1
		}
		if len(cur) > 0 && size+n > budget {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, unit...)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// splitTree writes the tree of base with paths taken from commit from (a
// path from does not have is removed) and returns its id. A temporary
// index; the worktree's own index and files are untouched. Removals go
// first and additions replace what is in their way, so a path that turned
// from a file into a directory (or back) on either side builds.
func splitTree(ctx context.Context, ws, base, from string, paths []string) (string, error) {
	tmp, err := os.MkdirTemp("", "itervox-split-index-")
	if err != nil {
		return "", fmt.Errorf("orchestrator: temp index: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	index := filepath.Join(tmp, "index")
	git := func(args ...string) ([]byte, error) {
		cmd := gitexec.Command(ctx, ws, args...)
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("orchestrator: git %s: %s", args[0], gitStderr(err))
		}
		return out, nil
	}
	if _, err := git("read-tree", base); err != nil {
		return "", err
	}
	listing, err := git("ls-tree", "-r", "-z", "--full-tree", from)
	if err != nil {
		return "", err
	}
	entries := map[string]string{} // path → "mode type id"
	for _, rec := range bytes.Split(listing, []byte{0}) {
		if meta, p, ok := strings.Cut(string(rec), "\t"); ok {
			entries[p] = meta
		}
	}
	var add, remove []string
	for _, p := range paths {
		if meta, ok := entries[p]; ok {
			mode, rest, _ := strings.Cut(meta, " ")
			_, id, _ := strings.Cut(rest, " ")
			add = append(add, "--cacheinfo", mode+","+id+","+p)
		} else {
			remove = append(remove, p)
		}
	}
	// The paths go to update-index as arguments, never on its stdin, in
	// batches within splitArgBudget.
	for _, b := range argBatches(remove, 1, splitArgBudget) {
		if _, err := git(append([]string{"update-index", "--force-remove", "--"}, b...)...); err != nil {
			return "", err
		}
	}
	for _, b := range argBatches(add, 2, splitArgBudget) {
		if _, err := git(append([]string{"update-index", "--add", "--replace"}, b...)...); err != nil {
			return "", err
		}
	}
	out, err := git("write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// commitTree commits tree on parent with msg and the head commit's author.
func commitTree(ctx context.Context, ws, tree, parent, msg string, env []string) (string, error) {
	cmd := gitexec.Command(ctx, ws, "commit-tree", tree, "-p", parent, "-m", msg)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("orchestrator: git commit-tree: %s", gitStderr(err))
	}
	return strings.TrimSpace(string(out)), nil
}

// treeOf is commit's tree id ("" when it cannot be read).
func treeOf(ctx context.Context, ws, commit string) string {
	return strings.TrimSpace(gitOutput(ctx, ws, "rev-parse", "--verify", "--quiet", commit+"^{tree}"))
}

// haveCommit reports whether the repository has commit, fetching branch
// from origin when it does not (a worktree recreated after a restart).
func haveCommit(ctx context.Context, ws, commit, branch string) bool {
	has := func() bool {
		return gitexec.Command(ctx, ws, "cat-file", "-e", commit+"^{commit}").Run() == nil
	}
	if has() {
		return true
	}
	_ = gitexec.Command(ctx, ws, "fetch", "--quiet", "--no-tags", "origin", "refs/heads/"+branch).Run()
	return has()
}

// removeTempWorktree removes a split's temporary worktree wt of ws and its
// directory dir. -f -f also removes a worktree an interrupted add left
// locked.
func removeTempWorktree(ws, wt, dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = gitexec.Command(ctx, ws, "worktree", "remove", "-f", "-f", wt).Run()
	_ = gitexec.Command(ctx, ws, "worktree", "prune").Run()
	_ = os.RemoveAll(dir)
}

func gitOutput(ctx context.Context, ws string, args ...string) string {
	out, err := gitexec.Command(ctx, ws, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
