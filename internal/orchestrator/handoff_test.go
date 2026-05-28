package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/workspace"
)

func TestHandoffRunTimestampIsFilenameSafe(t *testing.T) {
	ts := handoffRunTimestamp(time.Date(2026, 5, 25, 14, 30, 45, 0, time.UTC))
	assert.Equal(t, "2026-05-25T14-30-45.000Z", ts)
	// Round-trip the timestamp through path.Join to confirm no characters
	// require escaping in a filename.
	path := filepath.Join("handoff", ts+"_researcher.md")
	assert.NotContains(t, path, ":", "colons in filename break on Windows / cause shell quoting")
}

// G24: rapid retries within the same wall-clock second must produce
// distinct timestamps so attempt N's partial-handoff is not overwritten
// when attempt N+1's partial-handoff is renamed.
func TestHandoffRunTimestampDisambiguatesWithinSameSecond(t *testing.T) {
	base := time.Date(2026, 5, 25, 14, 30, 45, 0, time.UTC)
	a := handoffRunTimestamp(base)
	b := handoffRunTimestamp(base.Add(120 * time.Millisecond))
	assert.NotEqual(t, a, b,
		"two timestamps within the same second must differ at millisecond precision")
	assert.Less(t, a, b,
		"lexicographic order must equal chronological order for sort.Strings to work")
}

func TestHandoffPathForIncludesProfileAndTimestamp(t *testing.T) {
	ts := "2026-05-25T14-30-45Z"
	path := handoffPathFor(ts, "backend-builder")
	assert.Equal(t, filepath.Join(".itervox", "handoff", "2026-05-25T14-30-45Z_backend-builder.md"), path)
}

func TestHandoffPathForSlugifiesSpaces(t *testing.T) {
	path := handoffPathFor("2026-05-25T14-30-45Z", "story writer")
	assert.Equal(t, filepath.Join(".itervox", "handoff", "2026-05-25T14-30-45Z_story-writer.md"), path)
}

func TestHandoffPathForEmptyProfileFallback(t *testing.T) {
	path := handoffPathFor("2026-05-25T14-30-45Z", "")
	assert.Equal(t, filepath.Join(".itervox", "handoff", "2026-05-25T14-30-45Z_agent.md"), path)
}

func TestBuildRunContextBlockContainsBindings(t *testing.T) {
	block := buildRunContextBlock("2026-05-25T14-30-45Z", ".itervox/handoff/2026-05-25T14-30-45Z_researcher.md")
	assert.Contains(t, block, "## Run Context")
	assert.Contains(t, block, "run.timestamp")
	assert.Contains(t, block, "2026-05-25T14-30-45Z")
	assert.Contains(t, block, "run.handoff_path")
	assert.Contains(t, block, "2026-05-25T14-30-45Z_researcher.md")
}

func TestBuildHandoffContextBlockEmptyWorkspace(t *testing.T) {
	ws := t.TempDir()
	// No handoff dir at all.
	assert.Empty(t, buildHandoffContextBlock(ws, 0))
}

func TestBuildHandoffContextBlockEmptyDir(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws, HandoffDirRelPath), 0o755))
	// Empty handoff dir.
	assert.Empty(t, buildHandoffContextBlock(ws, 0))
}

func TestBuildHandoffContextBlockEmptyWorkspacePath(t *testing.T) {
	assert.Empty(t, buildHandoffContextBlock("", 0))
}

func writeHandoff(t *testing.T, ws, name, body string) {
	t.Helper()
	dir := filepath.Join(ws, HandoffDirRelPath)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
}

func TestBuildHandoffContextBlockChronologicalOrder(t *testing.T) {
	ws := t.TempDir()
	// Write out of order; sort should restore chronological order.
	writeHandoff(t, ws, "2026-05-25T15-00-00Z_spec-writer.md", "spec body")
	writeHandoff(t, ws, "2026-05-25T14-30-00Z_researcher.md", "research body")
	writeHandoff(t, ws, "2026-05-25T14-45-00Z_story-writer.md", "story body")

	block := buildHandoffContextBlock(ws, 0)
	assert.Contains(t, block, "## Prior Agent Handoffs")

	researcherIdx := strings.Index(block, "research body")
	storyIdx := strings.Index(block, "story body")
	specIdx := strings.Index(block, "spec body")

	require.True(t, researcherIdx >= 0)
	require.True(t, storyIdx >= 0)
	require.True(t, specIdx >= 0)
	assert.Less(t, researcherIdx, storyIdx, "researcher should appear before story (chronological)")
	assert.Less(t, storyIdx, specIdx, "story should appear before spec (chronological)")
}

func TestBuildHandoffContextBlockIncludesPartialMarker(t *testing.T) {
	ws := t.TempDir()
	writeHandoff(t, ws, "2026-05-25T14-30-00Z_researcher.md", "complete deliverable")
	writeHandoff(t, ws, "2026-05-25T15-00-00Z_backend.partial.md", "got interrupted halfway")

	block := buildHandoffContextBlock(ws, 0)
	assert.Contains(t, block, "complete deliverable")
	assert.Contains(t, block, "got interrupted halfway")
	assert.Contains(t, block, "(partial — prior agent exited before completing this deliverable)")
}

func TestBuildHandoffContextBlockBudgetDropsOldest(t *testing.T) {
	ws := t.TempDir()
	bigBody := strings.Repeat("x", 600)
	writeHandoff(t, ws, "2026-05-25T14-00-00Z_a.md", bigBody)
	writeHandoff(t, ws, "2026-05-25T15-00-00Z_b.md", bigBody)
	writeHandoff(t, ws, "2026-05-25T16-00-00Z_c.md", bigBody)

	// Budget tight enough to force dropping the oldest.
	block := buildHandoffContextBlock(ws, 1500)
	assert.Contains(t, block, "earlier handoffs truncated")
	assert.NotContains(t, block, "2026-05-25T14-00-00Z_a.md")
	assert.Contains(t, block, "2026-05-25T16-00-00Z_c.md")
}

func TestBuildHandoffContextBlockKeepsAtLeastOneFile(t *testing.T) {
	ws := t.TempDir()
	bigBody := strings.Repeat("x", 5000)
	writeHandoff(t, ws, "2026-05-25T14-00-00Z_a.md", bigBody)

	// Budget far below the single file's size — we still keep the file.
	block := buildHandoffContextBlock(ws, 100)
	assert.Contains(t, block, "2026-05-25T14-00-00Z_a.md")
}

func TestBuildHandoffContextBlockSkipsNonMarkdownAndDirs(t *testing.T) {
	ws := t.TempDir()
	dir := filepath.Join(ws, HandoffDirRelPath)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir"), 0o755))
	writeHandoff(t, ws, "2026-05-25T14-00-00Z_kept.md", "kept body")

	block := buildHandoffContextBlock(ws, 0)
	assert.Contains(t, block, "kept body")
	assert.NotContains(t, block, "ignored")
}

func TestMarkHandoffPartialRenamesExistingFile(t *testing.T) {
	ws := t.TempDir()
	writeHandoff(t, ws, "2026-05-25T14-00-00Z_backend.md", "in-flight work")
	rel := filepath.Join(HandoffDirRelPath, "2026-05-25T14-00-00Z_backend.md")

	require.NoError(t, markHandoffPartial(ws, rel))

	_, err := os.Stat(filepath.Join(ws, rel))
	assert.True(t, os.IsNotExist(err), "original file should be gone")
	body, err := os.ReadFile(filepath.Join(ws, HandoffDirRelPath, "2026-05-25T14-00-00Z_backend.partial.md"))
	require.NoError(t, err)
	assert.Equal(t, "in-flight work", string(body))
}

func TestMarkHandoffPartialNoOpWhenFileMissing(t *testing.T) {
	ws := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ws, HandoffDirRelPath), 0o755))
	rel := filepath.Join(HandoffDirRelPath, "2026-05-25T14-00-00Z_backend.md")

	// Agent never wrote a handoff — should be a clean no-op, not an error.
	require.NoError(t, markHandoffPartial(ws, rel))
}

func TestMarkHandoffPartialNoOpWhenAlreadyPartial(t *testing.T) {
	ws := t.TempDir()
	writeHandoff(t, ws, "2026-05-25T14-00-00Z_backend.partial.md", "already partial")
	rel := filepath.Join(HandoffDirRelPath, "2026-05-25T14-00-00Z_backend.partial.md")

	require.NoError(t, markHandoffPartial(ws, rel))

	// File should still exist with same name.
	body, err := os.ReadFile(filepath.Join(ws, rel))
	require.NoError(t, err)
	assert.Equal(t, "already partial", string(body))
}

func TestMarkHandoffPartialEmptyArgs(t *testing.T) {
	require.NoError(t, markHandoffPartial("", "anything"))
	require.NoError(t, markHandoffPartial("/tmp", ""))
}

func TestMarkLatestHandoffPartialNoFiles(t *testing.T) {
	ws := t.TempDir()
	// No handoff dir at all — should be a clean no-op.
	require.NoError(t, markLatestHandoffPartial(ws, "backend", time.Time{}))
}

func TestMarkLatestHandoffPartialNoMatchingProfile(t *testing.T) {
	ws := t.TempDir()
	writeHandoff(t, ws, "2026-05-25T14-00-00Z_researcher.md", "research")
	writeHandoff(t, ws, "2026-05-25T15-00-00Z_story-writer.md", "story")

	require.NoError(t, markLatestHandoffPartial(ws, "backend", time.Time{}))

	// Other roles' files untouched.
	_, err := os.Stat(filepath.Join(ws, HandoffDirRelPath, "2026-05-25T14-00-00Z_researcher.md"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(ws, HandoffDirRelPath, "2026-05-25T15-00-00Z_story-writer.md"))
	require.NoError(t, err)
}

func TestMarkLatestHandoffPartialPicksMostRecent(t *testing.T) {
	ws := t.TempDir()
	writeHandoff(t, ws, "2026-05-25T14-00-00Z_backend.md", "first attempt")
	// Sleep to ensure mtime distinction even on filesystems with coarse resolution.
	time.Sleep(20 * time.Millisecond)
	writeHandoff(t, ws, "2026-05-25T15-00-00Z_backend.md", "second attempt")

	require.NoError(t, markLatestHandoffPartial(ws, "backend", time.Time{}))

	// Older one stays intact.
	_, err := os.Stat(filepath.Join(ws, HandoffDirRelPath, "2026-05-25T14-00-00Z_backend.md"))
	require.NoError(t, err, "older handoff should be preserved as-is")

	// Newer one is now partial.
	_, err = os.Stat(filepath.Join(ws, HandoffDirRelPath, "2026-05-25T15-00-00Z_backend.md"))
	assert.True(t, os.IsNotExist(err), "newer handoff should have been renamed")
	body, err := os.ReadFile(filepath.Join(ws, HandoffDirRelPath, "2026-05-25T15-00-00Z_backend.partial.md"))
	require.NoError(t, err)
	assert.Equal(t, "second attempt", string(body))
}

func TestMarkLatestHandoffPartialIgnoresAlreadyPartialFiles(t *testing.T) {
	ws := t.TempDir()
	writeHandoff(t, ws, "2026-05-25T15-00-00Z_backend.partial.md", "prior partial")

	require.NoError(t, markLatestHandoffPartial(ws, "backend", time.Time{}))

	// Already-partial file is not in the glob (suffix is `_backend.md`, not
	// `_backend.partial.md`). Should remain.
	body, err := os.ReadFile(filepath.Join(ws, HandoffDirRelPath, "2026-05-25T15-00-00Z_backend.partial.md"))
	require.NoError(t, err)
	assert.Equal(t, "prior partial", string(body))
}

func TestMarkLatestHandoffPartialEmptyWorkspace(t *testing.T) {
	require.NoError(t, markLatestHandoffPartial("", "backend", time.Time{}))
}

func TestMarkLatestHandoffPartialSlugifiesProfileName(t *testing.T) {
	ws := t.TempDir()
	writeHandoff(t, ws, "2026-05-25T14-00-00Z_story-writer.md", "story")

	require.NoError(t, markLatestHandoffPartial(ws, "story writer", time.Time{}))

	_, err := os.Stat(filepath.Join(ws, HandoffDirRelPath, "2026-05-25T14-00-00Z_story-writer.md"))
	assert.True(t, os.IsNotExist(err), "spaces in profile name should slugify to hyphens, matching the on-disk filename")
}

// stubWorkspaceProvider returns a fixed workspace path. Implements the
// workspace.Provider interface for whitebox tests of orchestrator methods
// that depend on workspace resolution.
type stubWorkspaceProvider struct {
	path string
}

func (s *stubWorkspaceProvider) EnsureWorkspace(_ context.Context, _, _ string) (workspace.Workspace, error) {
	return workspace.Workspace{Path: s.path}, nil
}

func (s *stubWorkspaceProvider) RemoveWorkspace(_ context.Context, _, _ string) error {
	return nil
}

func (s *stubWorkspaceProvider) ResolvePath(_ string) string {
	return s.path
}

// White-box wiring test: o.markStalledHandoffPartial must resolve the
// workspace via o.workspace.ResolvePath and call markLatestHandoffPartial
// with the run entry's profile name. The full event-loop integration is
// implicit; this exercises the orchestrator method directly so the wiring
// is covered without flakiness from the stall watchdog timing.
func TestOrchestratorMarkStalledHandoffPartialWiring(t *testing.T) {
	ws := t.TempDir()
	writeHandoff(t, ws, "2026-05-26T11-00-00Z_implementer.md", "in-flight work")

	o := &Orchestrator{workspace: &stubWorkspaceProvider{path: ws}}
	runEntry := &RunEntry{ProfileName: "implementer"}
	issue := domain.Issue{Identifier: "ENG-42"}

	o.markStalledHandoffPartial(runEntry, issue)

	// Original file should be gone.
	_, err := os.Stat(filepath.Join(ws, HandoffDirRelPath, "2026-05-26T11-00-00Z_implementer.md"))
	assert.True(t, os.IsNotExist(err), "in-flight file should have been renamed")

	// .partial.md should exist with original content.
	body, err := os.ReadFile(filepath.Join(ws, HandoffDirRelPath, "2026-05-26T11-00-00Z_implementer.partial.md"))
	require.NoError(t, err)
	assert.Equal(t, "in-flight work", string(body))
}

// Companion white-box test for the TerminalFailed branch's rename hook.
func TestOrchestratorMarkFailedHandoffPartialWiring(t *testing.T) {
	ws := t.TempDir()
	writeHandoff(t, ws, "2026-05-26T11-00-00Z_implementer.md", "failed run")

	o := &Orchestrator{workspace: &stubWorkspaceProvider{path: ws}}
	runEntry := &RunEntry{ProfileName: "implementer"}
	issue := domain.Issue{Identifier: "ENG-42"}

	o.markFailedHandoffPartial(runEntry, issue)

	_, err := os.Stat(filepath.Join(ws, HandoffDirRelPath, "2026-05-26T11-00-00Z_implementer.md"))
	assert.True(t, os.IsNotExist(err))
	body, err := os.ReadFile(filepath.Join(ws, HandoffDirRelPath, "2026-05-26T11-00-00Z_implementer.partial.md"))
	require.NoError(t, err)
	assert.Equal(t, "failed run", string(body))
}

// markStalledHandoffPartial must no-op cleanly when there is no workspace.
func TestOrchestratorMarkStalledHandoffPartialNoWorkspaceNoOp(t *testing.T) {
	o := &Orchestrator{} // o.workspace == nil
	runEntry := &RunEntry{ProfileName: "implementer"}
	issue := domain.Issue{Identifier: "ENG-42"}
	// Must not panic.
	o.markStalledHandoffPartial(runEntry, issue)
	o.markFailedHandoffPartial(runEntry, issue)
}

// Defensive: nil RunEntry must not panic the rename hooks.
func TestOrchestratorMarkHandoffPartialNilRunEntryNoOp(t *testing.T) {
	ws := t.TempDir()
	o := &Orchestrator{workspace: &stubWorkspaceProvider{path: ws}}
	o.markStalledHandoffPartial(nil, domain.Issue{Identifier: "X"})
	o.markFailedHandoffPartial(nil, domain.Issue{Identifier: "X"})
}
