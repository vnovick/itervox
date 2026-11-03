package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// CursorFileName is the hub's persisted feed position in StateDir.
const CursorFileName = "hub_feed_cursors.json"

// cursorFileVersion versions hub_feed_cursors.json.
const cursorFileVersion = 1

// cursorFile is the shape of hub_feed_cursors.json. Members and the inner
// Notifiers maps are keyed by the member's workflow path; the values are
// member feed seqs.
type cursorFile struct {
	Version int `json:"version"`
	// HeadSeq is the last hub seq handed out, so the merged sequence keeps
	// growing across restarts and failovers.
	HeadSeq int64 `json:"headSeq"`
	// Members is the last member seq merged, per member.
	Members map[string]int64 `json:"members"`
	// Notifiers is the last member seq each notifier handled, per member.
	Notifiers map[string]map[string]int64 `json:"notifiers"`
}

// cursors is the in-memory cursorFile, saved by the hub's saver.
type cursors struct {
	path string

	mu    sync.Mutex
	st    cursorFile
	dirty bool
}

// loadCursors reads path; a missing or unreadable file starts empty (an
// unreadable one is logged: the hub then starts members at their current
// process, which loses nothing new).
func loadCursors(path string) *cursors {
	c := &cursors{path: path, st: cursorFile{Version: cursorFileVersion}}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		slog.Warn("hub: cannot read the feed cursors; starting fresh", "path", path, "error", err)
	default:
		var st cursorFile
		if json.Unmarshal(data, &st) != nil || st.Version != cursorFileVersion {
			slog.Warn("hub: feed cursors file is not valid; starting fresh", "path", path)
		} else {
			c.st = st
		}
	}
	if c.st.Members == nil {
		c.st.Members = map[string]int64{}
	}
	if c.st.Notifiers == nil {
		c.st.Notifiers = map[string]map[string]int64{}
	}
	return c
}

func (c *cursors) headSeq() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st.HeadSeq
}

// member returns the member's merge cursor; ok is false for a member the
// hub has never merged.
func (c *cursors) member(wf string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.st.Members[wf]
	return v, ok
}

// lowestNotifier is the lowest of cursor and every notifier's cursor for wf.
func (c *cursors) lowestNotifier(wf string, cursor int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, per := range c.st.Notifiers {
		if v, ok := per[wf]; ok {
			cursor = min(cursor, v)
		}
	}
	return cursor
}

// initMember records a member seen for the first time: its merge cursor
// and, for each named notifier without one, the notifier's cursor start at
// base (the last event that is history, not news).
func (c *cursors) initMember(wf string, base int64, notifiers []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.Members[wf] = base
	for _, n := range notifiers {
		per := c.notifierMapLocked(n)
		if _, ok := per[wf]; !ok {
			per[wf] = base
		}
	}
	c.dirty = true
}

// adoptNotifier gives a notifier that has no cursor for a member the
// member's merge cursor, so a newly added notifier starts with new events
// instead of replaying history.
func (c *cursors) adoptNotifier(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	per := c.notifierMapLocked(name)
	for wf, seq := range c.st.Members {
		if _, ok := per[wf]; !ok {
			per[wf] = seq
			c.dirty = true
		}
	}
}

// resetMember follows a member whose feed restarted below the cursors.
func (c *cursors) resetMember(wf string, seq int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.Members[wf] = seq
	for _, per := range c.st.Notifiers {
		if _, ok := per[wf]; ok {
			per[wf] = seq
		}
	}
	c.dirty = true
}

// setMember moves the member's merge cursor and records the hub head.
func (c *cursors) setMember(wf string, seq, head int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.Members[wf] != seq || c.st.HeadSeq != head {
		c.st.Members[wf], c.st.HeadSeq = seq, head
		c.dirty = true
	}
}

// notifier returns notifier name's cursor for wf.
func (c *cursors) notifier(name, wf string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.st.Notifiers[name][wf]
	return v, ok
}

// setNotifier records that notifier name handled wf's event seq.
func (c *cursors) setNotifier(name, wf string, seq int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	per := c.notifierMapLocked(name)
	if per[wf] < seq {
		per[wf] = seq
		c.dirty = true
	}
}

func (c *cursors) notifierMapLocked(name string) map[string]int64 {
	per, ok := c.st.Notifiers[name]
	if !ok {
		per = map[string]int64{}
		c.st.Notifiers[name] = per
	}
	return per
}

// save writes the file when something changed (atomically, 0600).
func (c *cursors) save() error {
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	data, err := json.MarshalIndent(c.st, "", "  ")
	c.dirty = false
	c.mu.Unlock()
	if err != nil {
		return fmt.Errorf("hub: encode cursors: %w", err)
	}
	if err := atomicfs.WriteFile(c.path, append(data, '\n'), 0o600); err != nil {
		c.mu.Lock()
		c.dirty = true
		c.mu.Unlock()
		return fmt.Errorf("hub: save cursors: %w", err)
	}
	return nil
}
