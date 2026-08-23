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

func TestLoadSidecarAcceptsV1WithZeroConfidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dependencies.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"version": 1,
		"generatedAt": "2026-05-28T10:00:00Z",
		"profile": "deps-analyzer",
		"edges": [
			{"source": "ENG-1", "target": "ENG-2", "evidence": "body mentions depends on ENG-1", "inferredAt": "2026-05-28T10:00:00Z"}
		]
	}`), 0o644))

	sc, err := LoadSidecar(path)
	require.NoError(t, err)
	require.NotNil(t, sc, "v1 sidecars must still load under schema v2")
	require.Len(t, sc.Edges, 1)
	assert.Equal(t, 0.0, sc.Edges[0].Confidence, "v1 edges have no confidence field so it defaults to 0")
}

func TestLoadSidecarV2RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := SidecarPath(dir)
	original := &Sidecar{
		Version:     SidecarSchemaVersion,
		GeneratedAt: time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
		Profile:     "deps-analyzer",
		Edges: []InferredEdge{
			{Source: "ENG-1", Target: "ENG-2", Evidence: "body mentions depends on ENG-1", InferredAt: time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC), Confidence: 0.83},
		},
	}
	require.NoError(t, SaveSidecar(path, original))

	got, err := LoadSidecar(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got.Edges, 1)
	assert.Equal(t, 0.83, got.Edges[0].Confidence)
}

func TestLoadSidecarClampsConfidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dependencies.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"version": 2,
		"edges": [
			{"source": "A", "target": "B", "confidence": 1.7},
			{"source": "C", "target": "D", "confidence": -0.2}
		]
	}`), 0o644))

	sc, err := LoadSidecar(path)
	require.NoError(t, err)
	require.NotNil(t, sc)
	require.Len(t, sc.Edges, 2)
	assert.Equal(t, 1.0, sc.Edges[0].Confidence, "confidence above 1 clamps to 1.0")
	assert.Equal(t, 0.0, sc.Edges[1].Confidence, "confidence below 0 clamps to 0.0")
}

func TestLoadSidecarUnknownVersionNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dependencies.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":3,"edges":[]}`), 0o644))

	sc, err := LoadSidecar(path)
	require.NoError(t, err)
	assert.Nil(t, sc, "unknown schema versions must read as nil")
}

func TestIssueFingerprintContentOnly(t *testing.T) {
	base := IssueFingerprint("Fix login bug", "Users cannot log in with SSO")

	t.Run("identical content produces identical fingerprint", func(t *testing.T) {
		again := IssueFingerprint("Fix login bug", "Users cannot log in with SSO")
		assert.Equal(t, base, again)
	})

	t.Run("title change alters fingerprint", func(t *testing.T) {
		changedTitle := IssueFingerprint("Fix login bug for real", "Users cannot log in with SSO")
		assert.NotEqual(t, base, changedTitle)
	})

	t.Run("description change alters fingerprint", func(t *testing.T) {
		changedDesc := IssueFingerprint("Fix login bug", "Users cannot log in with SSO at all")
		assert.NotEqual(t, base, changedDesc)
	})

	t.Run("fingerprint has no state input, so issues differing only by state fingerprint equal", func(t *testing.T) {
		// IssueFingerprint takes no state parameter at all — two issues with
		// the same title/description but different tracker states (which the
		// caller never passes in) necessarily fingerprint identically.
		open := IssueFingerprint("Fix login bug", "Users cannot log in with SSO")
		done := IssueFingerprint("Fix login bug", "Users cannot log in with SSO")
		assert.Equal(t, open, done)
	})
}

func TestSidecarAnalyzedRoundTrip(t *testing.T) {
	t.Run("v2 sidecar with Analyzed persists and loads", func(t *testing.T) {
		dir := t.TempDir()
		path := SidecarPath(dir)
		analyzedAt := time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)
		original := &Sidecar{
			Version:     SidecarSchemaVersion,
			GeneratedAt: analyzedAt,
			Profile:     "deps-analyzer",
			Analyzed: map[string]AnalyzedIssue{
				"ENG-1": {Fingerprint: IssueFingerprint("Title", "Desc"), AnalyzedAt: analyzedAt},
			},
		}
		require.NoError(t, SaveSidecar(path, original))

		got, err := LoadSidecar(path)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotNil(t, got.Analyzed)
		entry, ok := got.Analyzed["ENG-1"]
		require.True(t, ok)
		assert.Equal(t, IssueFingerprint("Title", "Desc"), entry.Fingerprint)
		assert.True(t, analyzedAt.Equal(entry.AnalyzedAt))
	})

	t.Run("pre-4b sidecar without the field loads with nil map", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "dependencies.json")
		require.NoError(t, os.WriteFile(path, []byte(`{
			"version": 2,
			"generatedAt": "2026-05-28T10:00:00Z",
			"profile": "deps-analyzer",
			"edges": []
		}`), 0o644))

		sc, err := LoadSidecar(path)
		require.NoError(t, err)
		require.NotNil(t, sc)
		assert.Nil(t, sc.Analyzed, "pre-4b sidecar has no analyzed field, so it must load as nil map")
	})
}
