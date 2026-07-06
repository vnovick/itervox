package skills

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Cache is the in-memory home of the most recent Inventory plus the file
// mtimes that produced it. Refresh() rescans only when Stale() reports any
// tracked file's mtime has moved.
//
// Concurrency: Get() takes Lock long enough to refresh the derived Stale flag
// before returning the inventory pointer. Callers should treat the returned
// *Inventory as read-only. Refresh() takes Lock so concurrent reads block
// briefly during swap-in.
type Cache struct {
	mu        sync.RWMutex
	inventory *Inventory
	mtimes    map[string]int64
	tracked   []string // file paths whose mtime gates re-scan
}

// NewCache builds an empty cache. Refresh() must be called once before Get
// returns useful data.
func NewCache() *Cache {
	return &Cache{
		mtimes:  make(map[string]int64),
		tracked: nil,
	}
}

// Get returns the most recent inventory. Returns nil before the first Refresh.
// Safe to call from any goroutine.
func (c *Cache) Get() *Inventory {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inventory != nil {
		c.inventory.Stale = c.staleLocked()
	}
	return c.inventory
}

// Stale returns true if any tracked file's mtime has moved since the last
// successful Refresh. A file becoming missing also counts as stale.
func (c *Cache) Stale() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.staleLocked()
}

func (c *Cache) staleLocked() bool {
	for _, p := range c.tracked {
		fi, err := os.Stat(p)
		if err != nil {
			// Tracked file disappeared → stale.
			return true
		}
		if mt, ok := c.mtimes[p]; !ok || mt != fi.ModTime().UnixNano() {
			return true
		}
	}
	return false
}

// Refresh runs scanFn and atomically swaps in the result on success. If
// scanFn returns an error with a non-nil inventory, the best-effort inventory
// is still stored with Partial/ScanError set. If the inventory is nil, the
// previous inventory is preserved (last-good semantics) and the error is
// returned to the caller.
//
// trackedFiles is the slice of file paths whose mtime feeds Stale(). Pass the
// same set every call so the cache remains consistent across refreshes.
func (c *Cache) Refresh(scanFn func() (*Inventory, error), trackedFiles []string) error {
	inv, err := scanFn()
	if err != nil {
		if inv != nil {
			inv.Partial = true
			inv.ScanError = err.Error()
			c.store(inv, trackedFiles)
		}
		return err
	}
	if inv == nil {
		return errors.New("skills.Cache.Refresh: scanFn returned nil inventory")
	}
	inv.Partial = false
	inv.ScanError = ""
	c.store(inv, trackedFiles)
	return nil
}

func (c *Cache) store(inv *Inventory, trackedFiles []string) {
	trackedFiles = trackedInventoryFiles(inv, trackedFiles)
	mtimes := make(map[string]int64, len(trackedFiles))
	for _, p := range trackedFiles {
		fi, statErr := os.Stat(p)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				continue
			}
			// Surface unexpected stat errors but don't fail the whole refresh.
			continue
		}
		mtimes[p] = fi.ModTime().UnixNano()
	}
	c.mu.Lock()
	c.inventory = inv
	c.mtimes = mtimes
	c.tracked = append([]string(nil), trackedFiles...)
	c.mu.Unlock()
}

func trackedInventoryFiles(inv *Inventory, base []string) []string {
	seen := make(map[string]struct{}, len(base))
	out := make([]string, 0, len(base))
	add := func(path string) {
		if path == "" {
			return
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			return
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	for _, path := range base {
		add(path)
	}
	if inv == nil {
		return out
	}
	for _, skill := range inv.Skills {
		add(skill.FilePath)
	}
	for _, plugin := range inv.Plugins {
		add(plugin.FilePath)
		for _, skill := range plugin.Skills {
			add(skill.FilePath)
		}
		for _, agent := range plugin.Agents {
			add(agent.FilePath)
		}
		for _, command := range plugin.Commands {
			add(command.FilePath)
		}
	}
	for _, doc := range inv.Instructions {
		add(doc.FilePath)
	}
	return out
}

// TrackedPathsFor returns the set of files whose mtime should gate a re-scan.
// Useful for callers that want to populate `trackedFiles` before the first
// Refresh based on the project + home dirs.
func TrackedPathsFor(projectDir, homeDir string) []string {
	var paths []string
	if projectDir != "" {
		paths = append(paths,
			filepath.Join(projectDir, ".claude", "settings.json"),
			filepath.Join(projectDir, ".mcp.json"),
			filepath.Join(projectDir, "CLAUDE.md"),
			filepath.Join(projectDir, "AGENTS.md"),
		)
	}
	if homeDir != "" {
		paths = append(paths,
			filepath.Join(homeDir, ".claude", "settings.json"),
			filepath.Join(homeDir, ".claude", "CLAUDE.md"),
			filepath.Join(homeDir, ".mcp.json"),
			filepath.Join(homeDir, ".agents", ".skill-lock.json"),
		)
	}
	return paths
}
