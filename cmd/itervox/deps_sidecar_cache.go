package main

import (
	"os"
	"sync"

	"github.com/vnovick/itervox/internal/depsanalysis"
)

// depsSidecarCache reads `.itervox/dependencies.json` lazily, reloading only
// when the file's mtime changes. The snapshot builder runs this on every
// tick; in steady state the cached pointer is returned without re-reading.
type depsSidecarCache struct {
	path string

	mu      sync.Mutex
	loaded  *depsanalysis.Sidecar
	mtime   int64 // ModTime().UnixNano(); 0 means "no successful load yet"
	missing bool  // last stat showed ErrNotExist — skip re-reads until mtime changes
}

func newDepsSidecarCache(path string) *depsSidecarCache {
	return &depsSidecarCache{path: path}
}

// Latest returns the current sidecar pointer, reloading from disk only when
// the file's mtime has changed since the last successful read. Failed loads
// (parse errors, schema mismatch) return (nil, nil) so the dashboard falls
// back to tracker-only edges.
func (c *depsSidecarCache) Latest() *depsanalysis.Sidecar {
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
		return nil
	}
	c.missing = false
	mtime := info.ModTime().UnixNano()
	if mtime == c.mtime && c.loaded != nil {
		return c.loaded
	}
	sc, err := depsanalysis.LoadSidecar(c.path)
	if err != nil {
		c.loaded = nil
		c.mtime = mtime
		return nil
	}
	c.loaded = sc
	c.mtime = mtime
	return sc
}
