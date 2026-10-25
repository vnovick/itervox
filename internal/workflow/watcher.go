package workflow

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
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
	mtime  time.Time
	size   int64
	hash   [32]byte // sha256; AbsentHash for an absent Optional file
	absent bool     // an Optional file that does not exist (#245)
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

// WatchedFile is one file a FileSetWatch watches.
type WatchedFile struct {
	// Path is the file. It need not exist when Optional is set.
	Path string
	// Loaded is the content hash of the bytes the running config was built
	// from (FileHash): the watcher's baseline for this file. AbsentHash for
	// an Optional file that did not exist when the config was loaded.
	Loaded [32]byte
	// Optional marks a file whose absence is a state, not an error (#245: the
	// user, project and local settings files). A missing Optional file is
	// polled by path, so creating it — and its directory — is a change, and
	// deleting it is a change that reloads without it. A missing required
	// file (WORKFLOW.md) is a stat error: the change stays pending, since
	// some editors unlink and recreate on save.
	Optional bool
}

// AbsentHash is the content hash FileHash gives a file that does not exist.
// A sha256 is never all zero bytes in practice, so it cannot collide with
// the hash of real content.
var AbsentHash [32]byte

// FileHash is the content hash the watcher and the self-write registry use:
// the sha256 of data, or AbsentHash when the file does not exist.
func FileHash(data []byte, exists bool) [32]byte {
	if !exists {
		return AbsentHash
	}
	return sha256.Sum256(data)
}

// FileSetWatch watches several files as one unit (#245): WORKFLOW.md and the
// user, project and local settings files. A change to any of them restarts
// one shared quiet period, so an edit that touches several files at once
// (`itervox init --update` moving keys out of WORKFLOW.md into a settings
// file) settles into exactly one onChange.
type FileSetWatch struct {
	Files []WatchedFile
	// Poll and Debounce override the stat interval and the quiet period;
	// zero means the package defaults (1 s and 2 s).
	Poll, Debounce time.Duration
	// Settled, when set, is called after every settle with the files it
	// judged (those that changed since the previous settle), whether or not
	// onChange ran. Tests use it to wait for a decision instead of sleeping.
	Settled func(judged []string)
}

// watchedState is the watcher's running view of one file.
type watchedState struct {
	WatchedFile
	current fileStamp
	settled [32]byte // the content hash as of the last settle (or Loaded)
	dirty   bool     // the stamp moved since the last settle
}

// Run blocks until ctx is cancelled, polling every file. Once no file has
// changed for the quiet period, each file that changed is judged on its own:
// one whose content moved from its last settled hash purely through this
// process's registered writes (selfwrite.go) is suppressed; any other is a
// change, and onChange is called once with every changed path.
//
// Loaded, not the first reading, is each file's baseline: a first reading
// that differs from it is a change pending from the start, judged at settle
// time like any other — a chain of this process's own writes on top of
// Loaded is suppressed, and anything else (an operator edit that landed
// between config.Load and this call) reloads (M1-close C2). The registry is
// also consulted for a foreign write the hash comparison cannot see
// (firstReadingSuspect). The first reading takes no lock: Loaded is the
// baseline, so there is no window left for a lock to close.
func (w FileSetWatch) Run(ctx context.Context, onChange func(changed []string)) error {
	files := make([]*watchedState, 0, len(w.Files))
	for _, f := range w.Files {
		st := &watchedState{WatchedFile: f, settled: f.Loaded}
		first, err := st.read(fileStamp{})
		if err != nil {
			slog.Warn("workflow watcher: initial stat failed", "path", f.Path, "error", err)
		}
		st.current = first
		st.dirty = first.hash != f.Loaded || firstReadingSuspect(f.Path, f.Loaded, first.hash, time.Now())
		files = append(files, st)
	}
	return w.loop(ctx, files, onChange)
}

// read stats one watched file; a missing Optional file reads as absent.
func (st *watchedState) read(prev fileStamp) (fileStamp, error) {
	next, err := stampOf(st.Path, prev)
	if err != nil && st.Optional && errors.Is(err, fs.ErrNotExist) {
		return fileStamp{absent: true}, nil
	}
	return next, err
}

func (w FileSetWatch) loop(ctx context.Context, files []*watchedState, onChange func(changed []string)) error {
	poll, debounce := w.Poll, w.Debounce
	if poll <= 0 {
		poll = pollInterval
	}
	if debounce <= 0 {
		debounce = debounceInterval
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	// changedAt is the moment any stamp last moved; zero means "settled,
	// nothing pending". Each further change, in any file, restarts the quiet
	// period, so a burst of saves produces exactly one onChange once every
	// file stops moving.
	var changedAt time.Time
	for _, st := range files {
		if st.dirty {
			changedAt = time.Now()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-ticker.C:
			moved := false
			for _, st := range files {
				next, err := st.read(st.current)
				if err != nil {
					// A stat error mid-edit is normal — some editors unlink
					// and recreate on save. Leave any pending change pending
					// rather than firing on a file we could not read.
					slog.Warn("workflow watcher: stat error", "path", st.Path, "error", err)
					continue
				}
				if next != st.current {
					slog.Debug("workflow watcher: file changed, waiting for it to settle",
						"path", st.Path, "old_mtime", st.current.mtime, "new_mtime", next.mtime,
						"debounce", debounce)
					st.current = next
					st.dirty = true
					moved = true
				}
			}
			if moved {
				changedAt = time.Now()
				continue
			}

			// Every stamp held steady this tick. Fire only once the quiet
			// period has elapsed since the LAST change, not since the first.
			if changedAt.IsZero() || time.Since(changedAt) < debounce {
				continue
			}
			changedAt = time.Time{}
			var judged, changed []string
			for _, st := range files {
				if !st.dirty {
					continue
				}
				st.dirty = false
				from := st.settled
				st.settled = st.current.hash
				judged = append(judged, st.Path)
				// Every dirty file is judged, so the links a suppressed
				// file used are consumed even when another file reloads.
				if consumeSelfWriteChain(st.Path, from, st.current.hash, time.Now()) {
					slog.Debug("workflow watcher: file settled on this process's own write, not reloading", "path", st.Path)
					continue
				}
				changed = append(changed, st.Path)
			}
			if len(changed) > 0 {
				slog.Debug("workflow watcher: files settled, reloading", "paths", changed)
				onChange(changed)
			}
			if w.Settled != nil {
				w.Settled(judged)
			}
		}
	}
}
