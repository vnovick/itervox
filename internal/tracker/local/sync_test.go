package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalIssueRewriteKeepsFrontMatterLayout (#118): a rewrite edits the
// front matter in place: key order, YAML comments and the spelling of
// unchanged values survive; a changed value is re-encoded where it was, a
// new key goes at the end, and a value that became empty is removed.
func TestLocalIssueRewriteKeepsFrontMatterLayout(t *testing.T) {
	ctx := context.Background()
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\n# Owned by the CLI team.\nstate: Todo   # not started\ntitle: \"Report\"\n"+
		"estimate: 3 # points\nlabels: [cli]\n---\n\nBody.\n")

	require.NoError(t, tr.UpdateIssueState(ctx, "ITX-1", "In Progress"))
	empty := []string{}
	remote := "#42"
	_, err := tr.ApplySync("ITX-1", SyncEdit{Labels: &empty, Remote: &remote})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "ITX-1.md"))
	require.NoError(t, err)
	assert.Equal(t, "---\n# Owned by the CLI team.\nstate: In Progress # not started\ntitle: \"Report\"\nestimate: 3 # points\n"+
		"updated: 2026-10-09T12:00:00Z\nremote: '#42'\n---\n\nBody.\n", string(data))
	is, err := tr.FetchIssueDetail(ctx, "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "In Progress", is.State)
	all, err := tr.SyncIssues()
	require.NoError(t, err)
	assert.Equal(t, "#42", all[0].Remote)
}

// TestLocalSyncSurfaceSkipsMalformedFiles (#118): SyncIssues leaves out a
// file that does not parse now, even one with a good earlier version, and
// ApplySync refuses to write it.
func TestLocalSyncSurfaceSkipsMalformedFiles(t *testing.T) {
	tr, dir := newTestTracker(t)
	path := filepath.Join(dir, "ITX-1.md")
	writeRaw(t, path, "---\ntitle: One\nstate: Todo\n---\n")
	writeRaw(t, filepath.Join(dir, "ITX-2.md"), "---\ntitle: Two\nstate: Todo\n---\n")
	all, err := tr.SyncIssues()
	require.NoError(t, err)
	require.Len(t, all, 2)

	writeRaw(t, path, "---\nstate: Todo\n---\n")
	all, err = tr.SyncIssues()
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "ITX-2", all[0].Identifier)
	title := "x"
	_, err = tr.ApplySync("ITX-1", SyncEdit{Title: &title})
	require.Error(t, err)
}

// TestCreateSyncIssueWritesPulledIssue (#118): a pulled issue gets the next
// number, its remote key and its comments in time order.
func TestCreateSyncIssueWritesPulledIssue(t *testing.T) {
	tr, _ := newTestTracker(t)
	p := 2
	t1 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	si, err := tr.CreateSyncIssue(SyncIssue{Title: "Pulled", State: "Todo", Description: "D", Priority: &p, Labels: []string{"cli"},
		Remote: "ENG-4", Comments: []SyncComment{{At: t1.Add(time.Hour), Author: "b", Body: "second"}, {At: t1, Author: "a", Body: "first"}}})
	require.NoError(t, err)
	assert.Equal(t, "ITX-1", si.Identifier)
	assert.Equal(t, "ENG-4", si.Remote)
	require.Len(t, si.Comments, 2)
	assert.Equal(t, "first", si.Comments[0].Body)
}
