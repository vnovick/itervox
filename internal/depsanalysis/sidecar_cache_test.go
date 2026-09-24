package depsanalysis

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSidecarCache_NilCacheReturnsNil(t *testing.T) {
	var c *SidecarCache
	assert.Nil(t, c.Latest())
}

func TestSidecarCacheMissingFileNil(t *testing.T) {
	dir := t.TempDir()
	c := NewSidecarCache(filepath.Join(dir, "missing.json"))
	assert.Nil(t, c.Latest())
}

func TestSidecarCacheReloadsOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	path := SidecarPath(dir)
	require.NoError(t, SaveSidecar(path, &Sidecar{
		Version:     SidecarSchemaVersion,
		GeneratedAt: time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
		Profile:     "deps-analyzer",
		Edges:       []InferredEdge{{Source: "A", Target: "B"}},
	}))

	c := NewSidecarCache(path)
	first := c.Latest()
	require.NotNil(t, first)
	assert.Len(t, first.Edges, 1)

	// Mutate the file and bump mtime explicitly so the OS reports a change
	// (TempDir filesystems on some macOS versions only have second-level mtime).
	require.NoError(t, SaveSidecar(path, &Sidecar{
		Version:     SidecarSchemaVersion,
		GeneratedAt: time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
		Profile:     "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "A", Target: "B"},
			{Source: "C", Target: "D"},
		},
	}))
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, future, future))

	second := c.Latest()
	require.NotNil(t, second)
	assert.Len(t, second.Edges, 2, "cache must reload when the file's mtime changes")
}

// TestSidecarCacheWarnsOncePerMtimeOnCorrupt is I1's regression guard: a
// corrupt sidecar must not panic, must not be silently swallowed forever, and
// must not be re-read/re-parsed on every Latest() call while the file stays
// unchanged. It pins the observable behaviour in three steps:
//  1. A corrupt file yields a nil sidecar (no panic).
//  2. Replacing the corrupt content with valid content but leaving the mtime
//     untouched (os.Chtimes back to the same timestamp) still returns nil —
//     this is only possible if the cache remembered the failed mtime and
//     skipped re-reading the file; if it re-read, it would see the now-valid
//     content and incorrectly return a non-nil sidecar.
//  3. Genuinely bumping the mtime (even with the same "fixed" content)
//     clears the memo and lets recovery through — proving the memo is keyed
//     on mtime, not wedged permanently.
func TestSidecarCacheWarnsOncePerMtimeOnCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := SidecarPath(dir)
	corruptAt := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o644))
	require.NoError(t, os.Chtimes(path, corruptAt, corruptAt))

	c := NewSidecarCache(path)

	// (1) Corrupt file -> nil, no panic.
	require.Nil(t, c.Latest())

	// (3-setup) Replace with valid content but pin the mtime back to the
	// exact same timestamp the failed attempt recorded.
	require.NoError(t, SaveSidecar(path, &Sidecar{
		Version: SidecarSchemaVersion,
		Edges:   []InferredEdge{{Source: "A", Target: "B"}},
	}))
	require.NoError(t, os.Chtimes(path, corruptAt, corruptAt))

	// (2) Identical mtime as the failed attempt -> the failed-mtime memo
	// must hold: still nil, because the cache must not re-read/re-parse.
	require.Nil(t, c.Latest(),
		"cache must remember the failed mtime and skip re-reading a file whose mtime hasn't changed")

	// (3) Bump the mtime -> the memo is keyed on mtime, so this must clear
	// it and let the now-valid content load through.
	fixedAt := corruptAt.Add(time.Hour)
	require.NoError(t, os.Chtimes(path, fixedAt, fixedAt))
	recovered := c.Latest()
	require.NotNil(t, recovered, "a genuinely new mtime must trigger a fresh read and allow recovery")
	assert.Len(t, recovered.Edges, 1)
}

func TestSidecarCache_ReturnsSameRefWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := SidecarPath(dir)
	require.NoError(t, SaveSidecar(path, &Sidecar{
		Version: SidecarSchemaVersion,
		Edges:   []InferredEdge{{Source: "A", Target: "B"}},
	}))

	c := NewSidecarCache(path)
	first := c.Latest()
	second := c.Latest()
	assert.Same(t, first, second, "back-to-back reads with no mtime change must return the same pointer")
}
