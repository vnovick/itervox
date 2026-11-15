package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
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
	// Two first dispatches must not clone into the same directory at once.
	unlock := lockRepo(barePath)
	defer unlock()

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
	// Dir "." keeps a relative cloneURL/barePath resolving exactly as before;
	// the scrubbed env is what stops an inherited GIT_DIR from mattering.
	cmd := gitexec.Command(ctx, ".", "clone", "--bare", cloneURL, barePath)
	cmd.Env = append(cmd.Env, "LANG=C", "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("bare: git clone --bare: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// git clone --bare does not set a fetch refspec by default, so
	// "git fetch" would be a no-op. Origin's branches go to
	// refs/remotes/origin/* (#195): mapping them onto refs/heads/* made every
	// fetch fail once a worktree had one of them checked out, and made new
	// branches track whatever local branch they started from. HEAD stays
	// valid: the clone's own refs/heads/<default> is kept and fast-forwarded
	// from origin after each fetch (see syncLocalBase).
	refspecCmd := gitexec.Command(ctx, ".", "-C", barePath,
		"config", "remote.origin.fetch", BareFetchRefspec)
	refspecCmd.Env = append(refspecCmd.Env, "LANG=C", "LC_ALL=C")
	if out, err := refspecCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("bare: set fetch refspec: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return barePath, nil
}

// repoLocks serializes git operations that write one shared repository's
// refs (#193): parallel dispatches each fetched into the shared bare clone at
// once, and git's ref locking made all but one fail with "cannot lock ref
// ... is at <sha> but expected <sha>". Keyed by the repository's canonical
// path; entries are never removed (one per repository the daemon uses). The
// lock is taken on worker goroutines, never on the orchestrator event loop.
var repoLocks sync.Map // canonical path → *sync.Mutex

// lockRepo locks the repository at path and returns its unlock function.
func lockRepo(path string) func() {
	mu, _ := repoLocks.LoadOrStore(canonicalPath(path), &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// FetchBare runs git fetch --all --prune in the bare clone to update all
// remote-tracking refs. Concurrent calls for one bare clone run one at a time.
//
// A clone made before #195 is migrated first (its legacy refspec rewritten),
// and the first successful fetch after that repairs branch upstreams that
// point at another branch. The outcome is recorded in BareFetchStatusFile
// for `itervox doctor`.
func FetchBare(ctx context.Context, barePath string) error {
	unlock := lockRepo(barePath)
	defer unlock()
	if err := migrateBareRefspec(ctx, barePath); err != nil {
		writeBareFetchStatus(barePath, err, time.Now())
		return err
	}
	cmd := gitexec.Command(ctx, ".", "-C", barePath, "fetch", "--all", "--prune")
	cmd.Env = append(cmd.Env, "LANG=C", "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		ferr := fmt.Errorf("bare: fetch: %w: %s", err, strings.TrimSpace(string(out)))
		writeBareFetchStatus(barePath, ferr, time.Now())
		return ferr
	}
	writeBareFetchStatus(barePath, nil, time.Now())
	repairUpstreamsIfPending(ctx, barePath)
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
	cmd := gitexec.Command(ctx, ".", "-C", barePath, "config", "--get", "remote.origin.url")
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
