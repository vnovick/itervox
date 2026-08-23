package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BareDir is the directory name for the bare clone inside workspace root.
const BareDir = ".bare"

// BarePath returns the absolute path to the bare clone directory.
func BarePath(root string) string {
	return filepath.Join(root, BareDir)
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// EnsureBareClone ensures a bare git clone exists at <root>/.bare/.
// If the directory already exists and contains a HEAD file, it is reused.
// If cloneURL is empty and the bare dir does not exist, an error is returned.
// Returns the absolute path to the bare clone.
func EnsureBareClone(ctx context.Context, root, cloneURL string) (string, error) {
	barePath := BarePath(root)

	// Already exists — reuse, but ONLY if it is the repository we were asked
	// for.
	//
	// Reusing on the mere presence of HEAD meant a bare clone of repository A
	// was silently handed to a daemon configured for repository B whenever the
	// two shared a workspace root: agents then branched from, committed to and
	// pushed the WRONG repository, and `git worktree add` metadata under
	// .bare/worktrees/<identifier> collided for two repos' issue #1. Sharing a
	// root is no longer the default, but an operator can still set one, so the
	// check belongs here rather than resting on the path convention.
	if info, err := os.Stat(filepath.Join(barePath, "HEAD")); err == nil && !info.IsDir() {
		if cloneURL == "" || bareRemoteMatches(ctx, barePath, cloneURL) {
			slog.Debug("bare: reusing existing bare clone", "path", barePath)
			return barePath, nil
		}
		// Refuse rather than re-clone over it: the existing clone may hold
		// another daemon's live worktrees, and deleting those would destroy
		// in-flight work. An explicit error names the collision so the
		// operator can give each project its own workspace.root.
		return "", fmt.Errorf(
			"bare: %s is a clone of a different repository than %q — "+
				"two projects are sharing one workspace.root; give each its own",
			barePath, cloneURL)
	}

	if cloneURL == "" {
		return "", fmt.Errorf("bare: %s does not exist and no clone_url configured", barePath)
	}

	// Ensure parent exists.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("bare: mkdir root: %w", err)
	}

	slog.Info("bare: cloning repository", "url", cloneURL, "path", barePath)
	cmd := exec.CommandContext(ctx, "git", "clone", "--bare", cloneURL, barePath)
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("bare: git clone --bare: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// git clone --bare does not set a fetch refspec by default, so
	// "git fetch" would be a no-op. Configure one that maps remote branches
	// directly to refs/heads/* (not refs/remotes/origin/*). This keeps HEAD
	// valid since HEAD points to refs/heads/<default_branch>.
	refspecCmd := exec.CommandContext(ctx, "git", "-C", barePath,
		"config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")
	refspecCmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	if out, err := refspecCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("bare: set fetch refspec: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return barePath, nil
}

// FetchBare runs git fetch --all --prune in the bare clone to update all remote refs.
func FetchBare(ctx context.Context, barePath string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", barePath, "fetch", "--all", "--prune")
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bare: fetch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// bareRemoteMatches reports whether the bare clone at barePath has cloneURL as
// its origin.
//
// Compared after normalisation because the same repository is spelled several
// ways — scp-style vs https, with and without a trailing ".git" — and treating
// those as different would force a needless re-clone. Unreadable config is
// treated as a MISMATCH: refusing costs a clear error, while wrongly reusing
// points a daemon at someone else's repository.
func bareRemoteMatches(ctx context.Context, barePath, cloneURL string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", barePath, "config", "--get", "remote.origin.url")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return normalizeRemoteURL(string(out)) == normalizeRemoteURL(cloneURL)
}

// normalizeRemoteURL reduces the common spellings of one repository to a
// comparable form: trailing whitespace and ".git", the scp-style "git@host:"
// prefix, and any "scheme://" prefix.
func normalizeRemoteURL(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimSuffix(u, ".git")

	scpStyle := true
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		scpStyle = false
	}

	// Split authority from path FIRST, then strip userinfo only inside the
	// authority. Searching the whole string for "@" let a path segment
	// impersonate a host: "https://evil.example.com/x@gitlab.com/org/repo"
	// normalised to the same value as "https://gitlab.com/org/repo", so a
	// bare clone of one repository would be reused for a different one —
	// exactly the substitution EnsureBareClone's check exists to prevent.
	authority, path := u, ""
	if slash := strings.Index(u, "/"); slash >= 0 {
		authority, path = u[:slash], u[slash:]
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	if scpStyle {
		// scp-style "host:path" — only the authority's colon separates them,
		// and it must not be confused with a "host:port".
		if colon := strings.Index(authority, ":"); colon >= 0 {
			authority, path = authority[:colon], "/"+authority[colon+1:]+path
		}
	}
	return strings.ToLower(strings.TrimSuffix(authority+path, "/"))
}
