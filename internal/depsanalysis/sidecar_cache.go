package depsanalysis

import (
	"log/slog"
	"os"
	"sync"
)

// SidecarCache reads `.itervox/dependencies.json` lazily, reloading only
// when the file's mtime changes. The snapshot builder runs this on every
// tick; in steady state the cached pointer is returned without re-reading.
type SidecarCache struct {
	path string

	mu      sync.Mutex
	loaded  *Sidecar
	mtime   int64 // ModTime().UnixNano() of the last read attempt (success or failure); 0 means "no attempt yet"
	missing bool  // last stat showed ErrNotExist — skip re-reads until mtime changes
	// loadFailed is true when the read attempt recorded in mtime failed to
	// parse. Combined with mtime this is the "warned/attempted once per
	// mtime" memo: while the file's mtime stays unchanged, Latest() returns
	// nil after only a stat — it does not re-read or re-parse the corrupt
	// file, and it does not log a second warning for the same mtime. A
	// changed mtime (the operator or agent fixed the file, or wrote a new
	// corrupt version) clears the fast path and triggers exactly one fresh
	// attempt — and, if that also fails, one fresh warning.
	loadFailed bool
}

// NewSidecarCache returns a cache that reads the sidecar at path.
func NewSidecarCache(path string) *SidecarCache {
	return &SidecarCache{path: path}
}

// Latest returns the current sidecar pointer, reloading from disk only when
// the file's mtime has changed since the last read attempt. Failed loads
// (parse errors, schema mismatch) return nil so the dashboard falls back to
// tracker-only edges; the failure is logged once (via slog.Warn) per distinct
// mtime rather than on every call, and a persistently corrupt file is not
// re-read/re-parsed on every tick — see loadFailed.
func (c *SidecarCache) Latest() *Sidecar {
	if c == nil || c.path == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := os.Stat(c.path)
	if err != nil {
		// Re-check on next tick — file may appear after the first analysis pass.
		c.loaded = nil
		c.mtime = 0
		c.missing = true
		c.loadFailed = false
		return nil
	}
	c.missing = false
	mtime := info.ModTime().UnixNano()
	if mtime == c.mtime && (c.loaded != nil || c.loadFailed) {
		// Fast path: either a good load or a remembered failure for this
		// exact mtime. Neither case re-reads the file.
		return c.loaded
	}
	sc, err := LoadSidecar(c.path)
	if err != nil {
		slog.Warn("depsanalysis: sidecar load failed", "path", c.path, "error", err)
		c.loaded = nil
		c.mtime = mtime
		c.loadFailed = true
		return nil
	}
	c.loaded = sc
	c.mtime = mtime
	c.loadFailed = false
	return sc
}
