package workflow

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"os"
	"time"
)

// pollInterval is how often Watch stats the file. A var so tests can shrink
// it; see export_test.go.
var pollInterval = 1 * time.Second

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

// WatchFrom is Watch with an explicit baseline: loaded is the sha256 of the
// bytes the running config was built from (Workflow.ContentHash, via
// config.Config.WorkflowHash). A first reading that differs from it is a
// change pending from the start, judged at settle time like any other: a
// chain of this process's own writes on top of loaded is suppressed, and
// anything else — an operator edit that landed between config.Load and this
// call — reloads (M1-close C2). Without it, such an edit became the
// watcher's baseline and was never reloaded, so the running config silently
// differed from WORKFLOW.md.
//
// The first reading takes no lock: the loaded hash, not the reading, is the
// baseline, so there is no window left for a lock to close.
func WatchFrom(ctx context.Context, path string, loaded [32]byte, onChange func()) error {
	first, err := stampOf(path, fileStamp{})
	if err != nil {
		slog.Warn("workflow watcher: initial stat failed", "path", path, "error", err)
	}
	return watch(ctx, path, loaded, first, true, onChange)
}

// checkRegistry enables WatchFrom's first-reading registry check
// (firstReadingSuspect); Watch's baseline is its own reading, so it has none.
func watch(ctx context.Context, path string, baseline [32]byte, current fileStamp, checkRegistry bool, onChange func()) error {
	// settled is the content hash as of the last settle (or the loaded
	// baseline): where a self-write chain must start from.
	settled := baseline

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// changedAt is the moment the stamp last moved; zero means "settled, nothing
	// pending". Each further change restarts the quiet period, so a burst of
	// saves produces exactly one onChange once the file stops moving. A first
	// reading that already differs from the baseline starts out pending.
	var changedAt time.Time
	if current.hash != baseline || (checkRegistry && firstReadingSuspect(path, baseline, current.hash, time.Now())) {
		changedAt = time.Now()
	}

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
				from := settled
				settled = current.hash
				if consumeSelfWriteChain(path, from, current.hash, time.Now()) {
					slog.Debug("workflow watcher: file settled on this process's own write, not reloading", "path", path)
					continue
				}
				slog.Debug("workflow watcher: file settled, reloading", "path", path)
				onChange()
			}
		}
	}
}
