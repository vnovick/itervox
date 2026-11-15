package referee

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
)

// gitTimeout bounds one git command.
const gitTimeout = 2 * time.Minute

// git runs `git args...` in dir and returns its stdout. A failure carries
// git's stderr.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := gitexec.Command(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out, nil
}

// resolveCommit is the full commit id ref names.
func resolveCommit(ctx context.Context, dir, ref string) (string, error) {
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	out, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%q is not a commit in this repository (fetch it, e.g. actions/checkout with fetch-depth: 0)", ref)
	}
	return strings.TrimSpace(string(out)), nil
}

// diffRange is what a pull request from head into base changes: the merge
// base of the two, and head.
type diffRange struct {
	Base, MergeBase, Head string
}

// resolveRange resolves base and head and their merge base.
func resolveRange(ctx context.Context, dir, base, head string) (diffRange, error) {
	b, err := resolveCommit(ctx, dir, base)
	if err != nil {
		return diffRange{}, err
	}
	h, err := resolveCommit(ctx, dir, head)
	if err != nil {
		return diffRange{}, err
	}
	out, err := git(ctx, dir, "merge-base", b, h)
	if err != nil {
		return diffRange{}, fmt.Errorf("no merge base between %s and %s: %w", base, head, err)
	}
	return diffRange{Base: b, MergeBase: strings.TrimSpace(string(out)), Head: h}, nil
}

// changedPaths lists the paths that differ between the merge base and head.
// --no-renames: a moved file is a deletion of its old path plus an addition,
// and both are classified.
func changedPaths(ctx context.Context, dir string, r diffRange) ([]string, error) {
	out, err := git(ctx, dir, "diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", r.MergeBase, r.Head, "--")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	return paths, nil
}

// showFile returns the content of path at commit, and false when the
// commit has no such file. Any other failure is an error.
func showFile(ctx context.Context, dir, commit, path string) ([]byte, bool, error) {
	ls, err := git(ctx, dir, "ls-tree", "-z", "--full-tree", commit, "--", path)
	if err != nil {
		return nil, false, err
	}
	entry, _, _ := bytes.Cut(ls, []byte{0})
	if len(entry) == 0 {
		return nil, false, nil
	}
	meta, _, _ := bytes.Cut(entry, []byte{'\t'})
	if f := strings.Fields(string(meta)); len(f) < 2 || f[1] != "blob" {
		return nil, false, fmt.Errorf("%s at %s is not a file", path, commit[:min(len(commit), 12)])
	}
	out, err := git(ctx, dir, "show", commit+":"+path)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
