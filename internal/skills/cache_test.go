package skills

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCache_GetReturnsNilBeforeRefresh(t *testing.T) {
	t.Parallel()
	c := NewCache()
	if c.Get() != nil {
		t.Errorf("expected nil inventory before Refresh, got %v", c.Get())
	}
}

func TestCache_RefreshSwapsInInventory(t *testing.T) {
	t.Parallel()
	c := NewCache()
	want := &Inventory{ScanTime: time.Now()}
	err := c.Refresh(func() (*Inventory, error) { return want, nil }, nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if c.Get() != want {
		t.Errorf("expected swapped-in pointer")
	}
}

func TestCache_RefreshErrorPreservesPrevious(t *testing.T) {
	t.Parallel()
	c := NewCache()
	first := &Inventory{ScanTime: time.Unix(1, 0)}
	if err := c.Refresh(func() (*Inventory, error) { return first, nil }, nil); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if err := c.Refresh(func() (*Inventory, error) { return nil, errors.New("scan failed") }, nil); err == nil {
		t.Fatal("expected error from failing scanFn")
	}
	if c.Get() != first {
		t.Errorf("last-good inventory should survive failed Refresh")
	}
}

func TestCache_RefreshStoresPartialInventoryAndClearsWarning(t *testing.T) {
	t.Parallel()
	c := NewCache()
	scanErr := errors.New("codex scanner failed")
	partial := &Inventory{
		ScanTime: time.Unix(1, 0),
		Skills:   []Skill{{Name: "ok", Provider: "claude"}},
	}

	err := c.Refresh(func() (*Inventory, error) { return partial, scanErr }, nil)
	if !errors.Is(err, scanErr) {
		t.Fatalf("Refresh error = %v, want %v", err, scanErr)
	}
	got := c.Get()
	if got != partial {
		t.Fatalf("expected partial inventory to be stored")
	}
	if !got.Partial {
		t.Fatalf("expected partial inventory warning flag")
	}
	if got.ScanError != scanErr.Error() {
		t.Fatalf("ScanError = %q, want %q", got.ScanError, scanErr.Error())
	}

	full := &Inventory{
		ScanTime: time.Unix(2, 0),
		Skills:   []Skill{{Name: "fresh", Provider: "codex"}},
	}
	if err := c.Refresh(func() (*Inventory, error) { return full, nil }, nil); err != nil {
		t.Fatalf("successful Refresh: %v", err)
	}
	got = c.Get()
	if got != full {
		t.Fatalf("expected successful inventory to replace partial")
	}
	if got.Partial || got.ScanError != "" {
		t.Fatalf("successful refresh should clear partial warning, got Partial=%v ScanError=%q", got.Partial, got.ScanError)
	}
}

func TestCache_RefreshNilInventoryIsError(t *testing.T) {
	t.Parallel()
	c := NewCache()
	err := c.Refresh(func() (*Inventory, error) { return nil, nil }, nil)
	if err == nil {
		t.Fatal("expected error when scanFn returns nil inventory + nil error")
	}
}

func TestCache_StaleDetectsMtimeChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tracked := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(tracked, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := NewCache()
	if err := c.Refresh(func() (*Inventory, error) { return &Inventory{}, nil }, []string{tracked}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if c.Stale() {
		t.Error("freshly refreshed cache should not be stale")
	}

	// Sleep enough to guarantee a different mtime — file systems vary in mtime
	// resolution; 10ms is conservative.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(tracked, []byte("v2"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !c.Stale() {
		t.Error("cache should be stale after tracked file rewrite")
	}
}

func TestCache_GetAnnotatesInventoryStaleStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tracked := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(tracked, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	c := NewCache()
	if err := c.Refresh(func() (*Inventory, error) { return &Inventory{}, nil }, []string{tracked}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := c.Get(); got == nil || got.Stale {
		t.Fatalf("fresh inventory Stale = %v, want false", got != nil && got.Stale)
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(tracked, []byte("v2"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if got := c.Get(); got == nil || !got.Stale {
		t.Fatalf("changed tracked file should mark inventory stale, got %+v", got)
	}
}

func TestCache_StaleDetectsMissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tracked := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(tracked, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := NewCache()
	if err := c.Refresh(func() (*Inventory, error) { return &Inventory{}, nil }, []string{tracked}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := os.Remove(tracked); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !c.Stale() {
		t.Error("cache should be stale after tracked file removal")
	}
}

func TestCache_StaleTracksInventorySkillAndPluginFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	skillPath := filepath.Join(dir, "SKILL.md")
	pluginPath := filepath.Join(dir, "plugin.json")
	childPath := filepath.Join(dir, "agent.md")
	for _, path := range []string{skillPath, pluginPath, childPath} {
		if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	c := NewCache()
	inv := &Inventory{
		Skills: []Skill{{Name: "alpha", FilePath: skillPath}},
		Plugins: []Plugin{{
			Name:     "plugin",
			FilePath: pluginPath,
			Agents:   []AgentDef{{Name: "agent", FilePath: childPath}},
		}},
	}
	if err := c.Refresh(func() (*Inventory, error) { return inv, nil }, nil); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if c.Stale() {
		t.Fatal("fresh inventory should not be stale")
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(skillPath, []byte("v2"), 0o644); err != nil {
		t.Fatalf("rewrite skill: %v", err)
	}
	if !c.Stale() {
		t.Fatal("editing discovered SKILL.md should mark cache stale")
	}

	if err := c.Refresh(func() (*Inventory, error) { return inv, nil }, nil); err != nil {
		t.Fatalf("Refresh 2: %v", err)
	}
	if err := os.Remove(pluginPath); err != nil {
		t.Fatalf("remove plugin: %v", err)
	}
	if !c.Stale() {
		t.Fatal("removing discovered plugin manifest should mark cache stale")
	}
}

func TestCache_ConcurrentGetWhileRefresh(t *testing.T) {
	t.Parallel()
	c := NewCache()
	if err := c.Refresh(func() (*Inventory, error) { return &Inventory{}, nil }, nil); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = c.Get()
			}
		}()
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = c.Refresh(func() (*Inventory, error) { return &Inventory{}, nil }, nil)
			}
		}()
	}
	wg.Wait()
}

func TestTrackedPathsFor_ReturnsBothScopes(t *testing.T) {
	t.Parallel()
	paths := TrackedPathsFor("/proj", "/home")
	if len(paths) < 4 {
		t.Errorf("expected at least 4 tracked paths, got %d", len(paths))
	}
	var foundProj, foundHome bool
	for _, p := range paths {
		if p == "/proj/.claude/settings.json" {
			foundProj = true
		}
		if p == "/home/.claude/settings.json" {
			foundHome = true
		}
	}
	if !foundProj || !foundHome {
		t.Errorf("expected proj+home tracked: proj=%v home=%v", foundProj, foundHome)
	}
}
