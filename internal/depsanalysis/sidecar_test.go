package depsanalysis

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadSidecar_MissingFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	sc, err := LoadSidecar(filepath.Join(dir, "does-not-exist.json"))
	require.NoError(t, err)
	assert.Nil(t, sc)
}

func TestSaveLoadSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := SidecarPath(dir)
	original := &Sidecar{
		Version:     SidecarSchemaVersion,
		GeneratedAt: time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
		Profile:     "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "ENG-1", Target: "ENG-2", Evidence: "body mentions depends on ENG-1", InferredAt: time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)},
		},
	}
	require.NoError(t, SaveSidecar(path, original))

	got, err := LoadSidecar(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, original.Version, got.Version)
	assert.Equal(t, original.Profile, got.Profile)
	require.Len(t, got.Edges, 1)
	assert.Equal(t, "ENG-1", got.Edges[0].Source)
	assert.Equal(t, "ENG-2", got.Edges[0].Target)
	assert.Equal(t, "body mentions depends on ENG-1", got.Edges[0].Evidence)
}

func TestLoadSidecar_RejectsOlderSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dependencies.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":0,"edges":[]}`), 0o644))

	sc, err := LoadSidecar(path)
	require.NoError(t, err)
	assert.Nil(t, sc, "older-schema sidecar must read as nil so the dashboard falls back to tracker-only edges")
}

func TestLoadSidecar_RejectsNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dependencies.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":99,"edges":[]}`), 0o644))

	sc, err := LoadSidecar(path)
	require.NoError(t, err)
	assert.Nil(t, sc, "newer-schema sidecar must read as nil — operator re-runs analyzer on upgrade")
}

func TestSaveSidecar_AssignsVersionWhenZero(t *testing.T) {
	dir := t.TempDir()
	path := SidecarPath(dir)
	sc := &Sidecar{GeneratedAt: time.Now().UTC(), Profile: "x"}
	require.NoError(t, SaveSidecar(path, sc))
	got, err := LoadSidecar(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, SidecarSchemaVersion, got.Version)
}

func TestSidecarPath_EmptyDirReturnsRelative(t *testing.T) {
	assert.Equal(t, SidecarRelativePath, SidecarPath(""))
}
