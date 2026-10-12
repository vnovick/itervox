package local

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultRescanInterval is how often Watch rescans the directory as a safety
// net for file events it missed (#133): a dropped event, a filesystem without
// notifications, or a directory created or recreated after Watch started.
const DefaultRescanInterval = 10 * time.Second

// watchDebounce lets an editor's burst of events for one save (write,
// chmod, rename) settle into one rescan.
const watchDebounce = 100 * time.Millisecond

// Watch calls changed whenever an issue file is added, edited or removed
// outside Itervox, until ctx ends: on a file event (debounced) or, as a safety
// net, on a rescan every rescan interval (DefaultRescanInterval when zero).
// The orchestrator wires changed to an immediate re-poll, so an edit is
// dispatched without waiting for the next poll. Itervox's own writes do not
// call it.
//
// While the directory is watched, reads are served from the entries the
// watch keeps current instead of rescanning on every call; writes still read
// the file first. When the directory is missing, removed or recreated, or
// file events are unavailable, reads rescan again and the watch is retried
// on each safety-net rescan.
func (t *Tracker) Watch(ctx context.Context, rescan time.Duration, changed func()) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("local tracker: file events unavailable; rescanning", "error", err)
		t.watch(ctx, nil, nil, rescan, changed, nil)
		return
	}
	defer func() { _ = w.Close() }()
	add := func() bool {
		// Only a directory: a file put at its path can be watched too, but
		// its events say nothing about the issues.
		if info, err := os.Stat(t.dir); err != nil || !info.IsDir() {
			return false
		}
		err := w.Add(t.dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("local tracker: cannot watch the issues directory; rescanning", "dir", t.dir, "error", err)
		}
		return err == nil
	}
	t.watch(ctx, w.Events, w.Errors, rescan, changed, add)
}

// watch is Watch's loop over an event source. add (re)starts the directory
// watch and reports whether it holds; nil means there are no file events.
func (t *Tracker) watch(ctx context.Context, events <-chan fsnotify.Event, errs <-chan error, rescan time.Duration, changed func(), add func() bool) {
	if rescan <= 0 {
		rescan = DefaultRescanInterval
	}
	ticker := time.NewTicker(rescan)
	defer ticker.Stop()
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	defer debounce.Stop()

	watching := false
	setWatching := func(on bool) {
		watching = on
		t.mu.Lock()
		t.watched = on
		t.mu.Unlock()
	}
	defer setWatching(false)

	var seen uint64
	check := func() {
		if !watching && add != nil {
			// Before the rescan, so an edit made after the directory
			// (re)appeared is either rescanned now or seen as an event.
			setWatching(add())
		}
		t.mu.Lock()
		_ = t.refreshLocked() // a failed scan is kept in scanErr: reads rescan and report it
		n := t.changes
		t.mu.Unlock()
		// Compared with the count, not the result of this rescan: a tracker
		// call may have rescanned first and found the change already.
		if n != seen {
			seen = n
			changed()
		}
	}
	if add != nil {
		setWatching(add())
	}
	// Load the entries the watch now serves; what is already on disk is not
	// a change.
	t.mu.Lock()
	_ = t.refreshLocked()
	seen = t.changes
	t.mu.Unlock()
	dir := filepath.Clean(t.dir)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				events, add = nil, nil // closed: rescans only
				setWatching(false)
				continue
			}
			if filepath.Clean(ev.Name) == dir && ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				// The directory itself went away, and its watch with it.
				setWatching(false)
				debounce.Reset(watchDebounce)
				continue
			}
			name := filepath.Base(ev.Name)
			if strings.HasSuffix(name, ".md") && !strings.HasPrefix(name, ".") {
				debounce.Reset(watchDebounce)
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			// An event may be lost (queue overflow): rescan now.
			slog.Warn("local tracker: file watch error; rescanning", "error", err)
			debounce.Reset(watchDebounce)
		case <-debounce.C:
			check()
		case <-ticker.C:
			check()
		}
	}
}
