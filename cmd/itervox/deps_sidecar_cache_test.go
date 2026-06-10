package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/depsanalysis"
)

func TestDepsSidecarCache_NilCacheReturnsNil(t *testing.T) {
	var c *depsSidecarCache
	assert.Nil(t, c.Latest())
}

func TestDepsSidecarCache_AbsentFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	c := newDepsSidecarCache(filepath.Join(dir, "missing.json"))
	assert.Nil(t, c.Latest())
}

func TestDepsSidecarCache_ReloadsOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	path := depsanalysis.SidecarPath(dir)
	require.NoError(t, depsanalysis.SaveSidecar(path, &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
		Profile:     "deps-analyzer",
		Edges:       []depsanalysis.InferredEdge{{Source: "A", Target: "B"}},
	}))

	c := newDepsSidecarCache(path)
	first := c.Latest()
	require.NotNil(t, first)
	assert.Len(t, first.Edges, 1)

	// Mutate the file and bump mtime explicitly so the OS reports a change
	// (TempDir filesystems on some macOS versions only have second-level mtime).
	require.NoError(t, depsanalysis.SaveSidecar(path, &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
		Profile:     "deps-analyzer",
		Edges: []depsanalysis.InferredEdge{
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

func TestDepsSidecarCache_ReturnsSameRefWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := depsanalysis.SidecarPath(dir)
	require.NoError(t, depsanalysis.SaveSidecar(path, &depsanalysis.Sidecar{
		Version: depsanalysis.SidecarSchemaVersion,
		Edges:   []depsanalysis.InferredEdge{{Source: "A", Target: "B"}},
	}))

	c := newDepsSidecarCache(path)
	first := c.Latest()
	second := c.Latest()
	assert.Same(t, first, second, "back-to-back reads with no mtime change must return the same pointer")
}
