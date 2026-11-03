package guardrails

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
)

// RefUpdate is one line of a pre-push hook's standard input.
type RefUpdate struct {
	LocalRef, LocalSHA, RemoteRef, RemoteSHA string
}

// ParseRefUpdates parses pre-push input: "<local ref> <local sha>
// <remote ref> <remote sha>" per line. Malformed lines are skipped.
func ParseRefUpdates(input string) []RefUpdate {
	var out []RefUpdate
	for _, line := range strings.Split(input, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 {
			continue
		}
		out = append(out, RefUpdate{LocalRef: f[0], LocalSHA: f[1], RemoteRef: f[2], RemoteSHA: f[3]})
	}
	return out
}

// Violation is one refused ref update or command.
type Violation struct {
	Kind    string
	Ref     string
	Message string
}

// Repo answers the history questions CheckPush asks of the pushing
// repository.
type Repo interface {
	// FastForward reports whether remoteSHA is an ancestor of localSHA, so
	// the push only adds commits. False when remoteSHA is not known
	// locally: git hands the hook an update it has not fetched only when
	// the push is forced.
	FastForward(remoteSHA, localSHA string) bool
	// CurrentBranch is the branch checked out ("" when detached).
	CurrentBranch() string
}

func zeroSHA(sha string) bool { return strings.Trim(sha, "0") == "" }

// CheckPush applies the push rules to a pre-push hook's ref updates:
//
//   - a tag push is refused;
//   - only the issue branch may be updated — or, once per run, a NEW branch
//     that is the one checked out and is not protected: the agent created
//     its own branch for the issue (Itervox then tracks it as the issue
//     branch). adopt names that branch; the caller records it only when
//     the whole push is allowed;
//   - a deletion, and an update that is not a fast-forward, is refused
//     unless allow_force_push.
func (p Policy) CheckPush(updates []RefUpdate, repo Repo) (violations []Violation, adopt string) {
	allowed := p.allowedBranches()
	canAdopt := p.adoptedBranch() == ""
	for _, u := range updates {
		ref := u.RemoteRef
		if strings.HasPrefix(ref, "refs/tags/") {
			violations = append(violations, Violation{Kind: KindTag, Ref: ref, Message: fmt.Sprintf(
				"guardrails: pushing tag %s is refused: agents do not push tags. Leave tags and releases to a human.", ref)})
			continue
		}
		branch, isBranch := strings.CutPrefix(ref, "refs/heads/")
		deleting, creating := zeroSHA(u.LocalSHA), zeroSHA(u.RemoteSHA)
		ok := isBranch && slices.Contains(allowed, branch)
		if isBranch && !ok && canAdopt && adopt == "" && creating && !deleting &&
			!p.protected(branch) && repo.CurrentBranch() == branch {
			adopt, ok = branch, true
		}
		switch {
		case !ok:
			violations = append(violations, Violation{Kind: KindBranch, Ref: ref, Message: p.branchMessage(ref, allowed)})
		case deleting && !p.AllowForcePush:
			violations = append(violations, Violation{Kind: KindDelete, Ref: ref, Message: fmt.Sprintf(
				"guardrails: deleting %s is refused (guardrails.allow_force_push is off). Leave branch cleanup to Itervox or a human.", ref)})
		case !deleting && !creating && !p.AllowForcePush && !repo.FastForward(u.RemoteSHA, u.LocalSHA):
			violations = append(violations, Violation{Kind: KindForcePush, Ref: ref, Message: fmt.Sprintf(
				"guardrails: force push to %s is refused: it would rewrite published history. Merge the base branch instead of rebasing and push a fast-forward, or ask the operator to set guardrails.allow_force_push.", ref)})
		}
	}
	if len(violations) > 0 {
		adopt = ""
	}
	return violations, adopt
}

func (p Policy) branchMessage(ref string, allowed []string) string {
	if len(allowed) == 0 {
		return fmt.Sprintf("guardrails: push to %s is refused: no issue branch is set for this session, so agents may only push a new branch they created and have checked out. Commit locally and leave pushing to a human.", ref)
	}
	return fmt.Sprintf("guardrails: push to %s is refused: agents may push only to the issue branch %q. Push your commits with `git push origin HEAD:refs/heads/%s`.",
		ref, allowed[0], allowed[0])
}

// gitRepo is Repo for the repository in dir, through gitexec.
type gitRepo struct{ dir string }

func (g gitRepo) FastForward(remoteSHA, localSHA string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return gitexec.Command(ctx, g.dir, "merge-base", "--is-ancestor", remoteSHA, localSHA).Run() == nil
}

func (g gitRepo) CurrentBranch() string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := gitexec.Command(ctx, g.dir, "symbolic-ref", "--short", "-q", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runPrePush is the pre-push hook: the push rules, then the repository's
// own pre-push hook with the same arguments and input. args are the hook's
// arguments (remote name, remote URL); env is the hook's environment as
// git gave it.
func runPrePush(p Policy, args, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
	input, err := io.ReadAll(stdin)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guardrails: cannot read the push: %v\n", err)
		return 1
	}
	dir, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guardrails: cannot resolve the repository: %v\n", err)
		return 1
	}
	violations, adopt := p.CheckPush(ParseRefUpdates(string(input)), gitRepo{dir: dir})
	if len(violations) > 0 {
		remote := ""
		if len(args) > 0 {
			remote = args[0] // the remote's name; never the URL, which may carry credentials
		}
		for _, v := range violations {
			_, _ = fmt.Fprintln(stderr, v.Message)
			newClient(p).report(Event{Kind: v.Kind, Tool: "git push", Command: displayCommand("git", []string{"push", remote, v.Ref})})
		}
		return 1
	}
	if adopt != "" {
		if err := p.adopt(adopt); err != nil {
			_, _ = fmt.Fprintf(stderr, "guardrails: cannot record branch %s: %v\n", adopt, err)
			return 1
		}
	}
	return chainHook(p.OrigHooksDir, "pre-push", args, env, input, stdout, stderr)
}

// chainHook runs the repository's own hook name, when it is executable,
// with the same arguments, environment and input.
func chainHook(origDir, name string, args, env []string, input []byte, stdout, stderr io.Writer) int {
	if origDir == "" {
		return 0
	}
	hook := filepath.Join(origDir, name)
	if info, err := os.Stat(hook); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return 0
	}
	cmd := exec.Command(hook, args...)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			return exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(stderr, "guardrails: the repository's %s hook failed: %v\n", name, err)
		return 1
	}
	return 0
}
