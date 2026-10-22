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
// net for file events it missed (#133) while it holds no healthy watch: a
// filesystem without notifications, or a directory missing, removed or
// recreated after Watch started.
const DefaultRescanInterval = 10 * time.Second

// HealthyRescanInterval is Watch's default safety-net rescan while the
// directory is watched (#210): file events deliver edits, so the rescan only
// catches a dropped event, and a 10 s stat of every issue file was most of
// an idle daemon's wakeups.
const HealthyRescanInterval = 60 * time.Second

// watchDebounce lets an editor's burst of events for one save (write,
// chmod, rename) settle into one rescan.
const watchDebounce = 100 * time.Millisecond

// Watch calls changed whenever an issue file is added, edited or removed
// outside Itervox, until ctx ends: on a file event (debounced) or, as a safety
// net, on a rescan every rescan interval. Zero picks the defaults:
// HealthyRescanInterval while the directory is watched, DefaultRescanInterval
// while it is not; a non-zero rescan is used for both.
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
		t.watch(ctx, nil, nil, rescan, healthyRescan(rescan), changed, nil)
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
	t.watch(ctx, w.Events, w.Errors, rescan, healthyRescan(rescan), changed, add)
}

// healthyRescan is the safety-net interval while watched for Watch's rescan
// argument.
func healthyRescan(rescan time.Duration) time.Duration {
	if rescan <= 0 {
		return HealthyRescanInterval
	}
	return rescan
}

// watchAddedHook runs right after the directory watch is added and before
// the rescan that lets the watch serve reads. Tests use it to read in that
// gap; it does nothing in production.
var watchAddedHook = func() {}

// watch is Watch's loop over an event source. add (re)starts the directory
// watch and reports whether it holds; nil means there are no file events.
// The safety-net rescan runs every healthy while the watch holds and every
// rescan while it does not.
func (t *Tracker) watch(ctx context.Context, events <-chan fsnotify.Event, errs <-chan error, rescan, healthy time.Duration, changed func(), add func() bool) {
	if rescan <= 0 {
		rescan = DefaultRescanInterval
	}
	if healthy <= 0 {
		healthy = rescan
	}
	ticker := time.NewTicker(rescan)
	defer ticker.Stop()
	interval := rescan
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
	// retime moves the safety net to the rate for the current watch health.
	retime := func() {
		want := rescan
		if watching {
			want = healthy
		}
		if want != interval {
			interval = want
			ticker.Reset(want)
		}
	}

	var seen uint64
	check := func() {
		on := watching
		if !watching && add != nil {
			// Before the rescan, so an edit made after the directory
			// (re)appeared is either rescanned now or seen as an event.
			on = add()
			watchAddedHook()
		}
		t.mu.Lock()
		_ = t.refreshLocked() // a failed scan is kept in scanErr: reads rescan and report it
		// Marked watched only together with the rescan (#133): a read
		// between the two would otherwise be served stale entries.
		watching, t.watched = on, on
		n := t.changes
		t.mu.Unlock()
		// Compared with the count, not the result of this rescan: a tracker
		// call may have rescanned first and found the change already.
		if n != seen {
			seen = n
			changed()
		}
	}
	on := false
	if add != nil {
		on = add()
		watchAddedHook()
	}
	// Load the entries the watch now serves; what is already on disk is not
	// a change. The watch only serves reads once they are loaded: a read
	// between add and this rescan must rescan itself, not see no entries.
	t.mu.Lock()
	_ = t.refreshLocked()
	watching, t.watched = on, on
	seen = t.changes
	t.mu.Unlock()
	retime()
	dir := filepath.Clean(t.dir)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				events, add = nil, nil // closed: rescans only
				setWatching(false)
				retime()
				continue
			}
			if filepath.Clean(ev.Name) == dir && ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				// The directory itself went away, and its watch with it.
				setWatching(false)
				retime()
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
			retime()
		case <-ticker.C:
			check()
			retime()
		}
	}
}
