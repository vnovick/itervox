package attest

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/gitexec"
)

// gitEnviron is the environment a check runs with: the daemon's, scrubbed
// of every variable that would point git at another repository.
func gitEnviron() []string { return gitexec.Environ() }

// TreeHash returns the git tree id of the working tree in dir as it is now:
// every tracked file and every untracked file git does not ignore, with
// excludes (repository paths) left out. It never touches the user's index:
// git runs with GIT_INDEX_FILE pointing at a private copy in a temporary
// directory, which is removed afterwards. The blobs `git add` writes land in
// the repository's object store, where unreferenced ones are pruned by gc
// like any other loose object.
//
// The copy is only a speed-up (git reuses its stat data instead of
// re-hashing every file); when it cannot be used, the hash is computed from
// an empty index and is the same.
func TreeHash(ctx context.Context, dir string, excludes []string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("attest: tree hash needs a directory")
	}
	tmp, err := os.MkdirTemp("", "itervox-attest-index-")
	if err != nil {
		return "", fmt.Errorf("attest: temp index: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	index := filepath.Join(tmp, "index")

	if real := userIndexPath(ctx, dir); real != "" {
		if copyFile(real, index) != nil {
			_ = os.Remove(index)
		}
	}
	tree, err := writeTree(ctx, dir, index, excludes)
	if err != nil && fileExists(index) {
		// A copied index git cannot use (a split index, say): start empty.
		_ = os.Remove(index)
		tree, err = writeTree(ctx, dir, index, excludes)
	}
	return tree, err
}

// writeTree stages the whole working tree into index and writes its tree.
func writeTree(ctx context.Context, dir, index string, excludes []string) (string, error) {
	git := func(args ...string) (string, error) {
		cmd := gitexec.Command(ctx, dir, args...)
		// gitexec scrubbed any inherited GIT_INDEX_FILE; this one is ours.
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("attest: git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := git("add", "-A", "--", ":/"); err != nil {
		return "", err
	}
	if len(excludes) > 0 {
		args := []string{"rm", "-r", "-q", "--cached", "--ignore-unmatch", "--"}
		for _, ex := range excludes {
			args = append(args, ":/"+strings.TrimPrefix(ex, "/"))
		}
		if _, err := git(args...); err != nil {
			return "", err
		}
	}
	return git("write-tree")
}

// CommitTreeHash returns the git tree id of commit rev in dir with excludes
// left out, comparable with TreeHash: for a clean working tree checked out
// at rev the two are equal. The daemon's own push (push: daemon) compares
// it with a run's seal, so it pushes only a commit whose tree the signed
// checks ran on. Like TreeHash it uses a private index and never touches
// the user's.
func CommitTreeHash(ctx context.Context, dir, rev string, excludes []string) (string, error) {
	if dir == "" || rev == "" {
		return "", fmt.Errorf("attest: commit tree hash needs a directory and a revision")
	}
	tmp, err := os.MkdirTemp("", "itervox-attest-index-")
	if err != nil {
		return "", fmt.Errorf("attest: temp index: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	index := filepath.Join(tmp, "index")
	git := func(args ...string) (string, error) {
		cmd := gitexec.Command(ctx, dir, args...)
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("attest: git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := git("read-tree", rev+"^{tree}"); err != nil {
		return "", err
	}
	if len(excludes) > 0 {
		args := []string{"rm", "-r", "-q", "--cached", "--ignore-unmatch", "--"}
		for _, ex := range excludes {
			args = append(args, ":/"+strings.TrimPrefix(ex, "/"))
		}
		if _, err := git(args...); err != nil {
			return "", err
		}
	}
	return git("write-tree")
}

// userIndexPath is the absolute path of dir's own index file ("" when
// there is none yet, or dir is not a git work tree).
func userIndexPath(ctx context.Context, dir string) string {
	out, err := gitexec.Command(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", "index").Output()
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(out))
	if !fileExists(p) {
		return ""
	}
	return p
}

// Head is the full HEAD commit of dir, or "" (no commit yet, not a git
// work tree).
func Head(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	out, err := gitexec.Command(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// copyFile copies src to dst (read-only on src).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
