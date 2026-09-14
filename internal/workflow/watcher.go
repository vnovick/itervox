package workflow

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"os"
	"time"
)

const pollInterval = 1 * time.Second

// debounceInterval is how long WORKFLOW.md must stay unchanged before a change
// is acted on.
//
// onChange tears the daemon's run loop down and back up — which rebinds the
// HTTP listener — so firing on the first changed byte makes a multi-part edit
// unusable: saving after each of three edits reloads three times, and a save
// mid-edit reloads on a half-finished file. Waiting for the file to settle
// coalesces a burst of saves into exactly one reload.
//
// A var rather than a const so tests can shrink it; see export_test.go.
var debounceInterval = 2 * time.Second

// fileStamp captures the identity of a file at a point in time.
type fileStamp struct {
	mtime time.Time
	size  int64
	hash  [32]byte // sha256
}

// stampOf returns a fileStamp for the file at path.
// prev is the previously known stamp; if mtime and size match prev,
// the sha256 is not recomputed and prev is returned unchanged (fast path).
func stampOf(path string, prev fileStamp) (fileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}

	mtime := fi.ModTime()
	size := fi.Size()

	// Fast path: mtime and size match — assume content is identical.
	if mtime.Equal(prev.mtime) && size == prev.size {
		return prev, nil
	}

	// Compute sha256 of file content.
	f, err := os.Open(path)
	if err != nil {
		return fileStamp{}, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fileStamp{}, err
	}

	var digest [32]byte
	copy(digest[:], h.Sum(nil))

	return fileStamp{mtime: mtime, size: size, hash: digest}, nil
}

// Watch monitors path for changes by polling every second with a content-hash
// stamp ({mtime, size, sha256}). onChange is called only when the stamp changes,
// preventing spurious reloads when an editor writes identical content.
// Blocks until ctx is cancelled. Returns any setup or context error.
func Watch(ctx context.Context, path string, onChange func()) error {
	// Capture the initial stamp so we don't fire on startup.
	current, err := stampOf(path, fileStamp{})
	if err != nil {
		slog.Warn("workflow watcher: initial stat failed", "path", path, "error", err)
		// Don't abort — the file may appear shortly; proceed with zero stamp.
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// changedAt is the moment the stamp last moved; zero means "settled, nothing
	// pending". Each further change restarts the quiet period, so a burst of
	// saves produces exactly one onChange once the file stops moving.
	var changedAt time.Time

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
			next, err := stampOf(path, current)
			if err != nil {
				// A stat error mid-edit is normal — some editors unlink and
				// recreate on save. Leave any pending change pending rather
				// than firing on a file we could not read.
				slog.Warn("workflow watcher: stat error", "path", path, "error", err)
				continue
			}

			if next != current {
				slog.Debug("workflow watcher: file changed, waiting for it to settle",
					"path", path, "old_mtime", current.mtime, "new_mtime", next.mtime,
					"debounce", debounceInterval)
				current = next
				changedAt = time.Now()
				continue
			}

			// Stamp held steady this tick. Fire only once the quiet period has
			// elapsed since the LAST change, not since the first.
			if !changedAt.IsZero() && time.Since(changedAt) >= debounceInterval {
				changedAt = time.Time{}
				slog.Debug("workflow watcher: file settled, reloading", "path", path)
				onChange()
			}
		}
	}
}
