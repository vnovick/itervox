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

	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// diffFixture is an orchestrator whose workspace for ENG-1 is a git repo:
// main has a base commit and has moved on since the branch was cut; the
// branch modifies, adds, deletes and commits a binary file, and the work
// tree holds an uncommitted edit and an untracked file. No Running entry,
// no run context: the diff must work for a finished issue too.
func diffFixture(t *testing.T) (*Orchestrator, string, string) {
	t.Helper()
	root := t.TempDir()
	cfg := automationBaseCfg()
	cfg.Workspace.Root = root
	cfg.Workspace.BaseBranch = "main"
	o := New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil, workspace.NewManager(cfg))
	dir := filepath.Join(root, "ENG-1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "user.email", "t@t")
	gitRun(t, dir, "config", "user.name", "t")
	write("a.txt", "one\ntwo\n")
	write("b.txt", "keep\n")
	write("gone.txt", "bye\n")
	write("big.txt", "seed\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	mergeBase := gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "checkout", "-q", "-b", "itervox/eng-1")
	write("a.txt", "one\nTWO\nthree\n")
	write("new.go", "package x\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bin.dat"), []byte{0, 1, 2, 0, 3}, 0o644))
	write("big.txt", strings.Repeat("a fairly long line of generated content\n", 10_000))
	require.NoError(t, os.Remove(filepath.Join(dir, "gone.txt")))
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "work")
	// main moves on after the branch was cut: a diff against main's tip
	// would show main-only.txt as deleted; against the merge base it must not.
	gitRun(t, dir, "checkout", "-q", "main")
	write("main-only.txt", "later\n")
	gitRun(t, dir, "add", "main-only.txt")
	gitRun(t, dir, "commit", "-q", "-m", "main moves on")
	gitRun(t, dir, "checkout", "-q", "itervox/eng-1")
	write("b.txt", "keep\nuncommitted\n")
	write("untracked.txt", "u1\nu2\nu3")
	return o, dir, mergeBase
}

// TestDiffListsFilesAgainstMergeBase (#215): the file list is the worktree
// (commits, uncommitted edits, untracked files) against the merge base of
// HEAD with the base branch — not against the base branch's tip — with each
// file's status and line counts; ?base= overrides the base, a bad one is
// refused, and an issue without a worktree is ErrNoWorktree.
func TestDiffListsFilesAgainstMergeBase(t *testing.T) {
	o, _, mergeBase := diffFixture(t)
	sum, err := o.WorktreeDiff(context.Background(), "ENG-1", "")
	require.NoError(t, err)
	assert.Equal(t, "main", sum.Base)
	assert.Equal(t, mergeBase, sum.MergeBase)

	byPath := map[string]workspace.DiffFile{}
	for _, f := range sum.Files {
		byPath[f.Path] = f
	}
	assert.NotContains(t, byPath, "main-only.txt", "diffed against the merge base, not main's tip")
	assert.Equal(t, workspace.DiffFile{Path: "a.txt", Status: workspace.DiffModified, Additions: 2, Deletions: 1}, byPath["a.txt"])
	assert.Equal(t, workspace.DiffFile{Path: "b.txt", Status: workspace.DiffModified, Additions: 1}, byPath["b.txt"], "uncommitted edits count")
	assert.Equal(t, workspace.DiffFile{Path: "new.go", Status: workspace.DiffAdded, Additions: 1}, byPath["new.go"])
	assert.Equal(t, workspace.DiffFile{Path: "gone.txt", Status: workspace.DiffDeleted, Deletions: 1}, byPath["gone.txt"])
	assert.Equal(t, workspace.DiffFile{Path: "bin.dat", Status: workspace.DiffAdded, Binary: true}, byPath["bin.dat"])
	assert.Equal(t, workspace.DiffFile{Path: "untracked.txt", Status: workspace.DiffUntracked, Additions: 3}, byPath["untracked.txt"])
	assert.Equal(t, 10_000, byPath["big.txt"].Additions)
	assert.Len(t, sum.Files, 7)

	explicit, err := o.WorktreeDiff(context.Background(), "ENG-1", "main")
	require.NoError(t, err)
	assert.Equal(t, mergeBase, explicit.MergeBase)

	for _, bad := range []string{"--output=/tmp/x", "main..HEAD", "HEAD~1^{tree}", "-p"} {
		_, err = o.WorktreeDiff(context.Background(), "ENG-1", bad)
		assert.ErrorIs(t, err, ErrBadDiffBase, bad)
	}
	_, err = o.WorktreeDiff(context.Background(), "ENG-1", "no-such-branch")
	assert.ErrorIs(t, err, workspace.ErrDiffBaseNotFound)
	_, err = o.WorktreeDiff(context.Background(), "ENG-404", "")
	assert.ErrorIs(t, err, ErrNoWorktree)
}

// TestHandoffsListTitlesAndPaths (#215): the run context's prior handoffs
// are the worktree's handoff files, oldest first, titled by their first
// heading (redacted), partials flagged.
func TestHandoffsListTitlesAndPaths(t *testing.T) {
	o, dir, _ := diffFixture(t)
	hd := filepath.Join(dir, HandoffDirRelPath)
	require.NoError(t, os.MkdirAll(hd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hd, "2026-01-02T00-00-00Z_reviewer.partial.md"), []byte("no heading here\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(hd, "2026-01-01T00-00-00Z_coder.md"), []byte("\n## Added the diff endpoint with ghp_"+strings.Repeat("Z", 36)+"\n\nbody\n"), 0o644))
	refs := o.Handoffs("ENG-1")
	require.Len(t, refs, 2)
	assert.Equal(t, ".itervox/handoff/2026-01-01T00-00-00Z_coder.md", refs[0].Path)
	assert.Contains(t, refs[0].Title, "Added the diff endpoint with")
	assert.NotContains(t, refs[0].Title, "ghp_ZZZZ")
	assert.Equal(t, "2026-01-02T00-00-00Z_reviewer.partial.md", refs[1].Title)
	assert.True(t, refs[1].Partial)
	assert.Nil(t, o.Handoffs("ENG-404"))
}

// TestDiffHunksCappedAndBinaryDetected (#215): a file's unified diff is cut
// at FileDiffMaxBytes on a line boundary and flagged truncated with the
// full size; a binary file has no patch; an untracked file diffs against
// nothing; a path outside the diff (including a traversal) never reaches
// git.
func TestDiffHunksCappedAndBinaryDetected(t *testing.T) {
	o, _, _ := diffFixture(t)
	ctx := context.Background()

	small, err := o.WorktreeFileDiff(ctx, "ENG-1", "", "a.txt")
	require.NoError(t, err)
	assert.False(t, small.Truncated)
	assert.Contains(t, small.Patch, "-two\n+TWO\n+three\n")
	assert.Equal(t, len(small.Patch), small.Bytes)

	big, err := o.WorktreeFileDiff(ctx, "ENG-1", "", "big.txt")
	require.NoError(t, err)
	assert.True(t, big.Truncated)
	assert.LessOrEqual(t, len(big.Patch), FileDiffMaxBytes)
	assert.Greater(t, big.Bytes, FileDiffMaxBytes)
	assert.True(t, strings.HasSuffix(big.Patch, "\n"), "cut on a line boundary")
	assert.Contains(t, big.Patch, "@@")

	bin, err := o.WorktreeFileDiff(ctx, "ENG-1", "", "bin.dat")
	require.NoError(t, err)
	assert.True(t, bin.Binary)
	assert.Empty(t, bin.Patch)

	untracked, err := o.WorktreeFileDiff(ctx, "ENG-1", "", "untracked.txt")
	require.NoError(t, err)
	assert.Contains(t, untracked.Patch, "+u1\n+u2\n+u3")

	for _, f := range []string{"main-only.txt", "../../etc/passwd", "/etc/passwd", "nope.txt"} {
		_, err = o.WorktreeFileDiff(ctx, "ENG-1", "", f)
		assert.ErrorIs(t, err, workspace.ErrDiffFileNotChanged, f)
	}
}

// TestDiffRunsOffEventLoop (#215): the diff runs in the caller's goroutine
// and never goes through the event loop — with no loop running and the
// event channel full, it still answers and sends nothing.
func TestDiffRunsOffEventLoop(t *testing.T) {
	o, _, _ := diffFixture(t)
	for len(o.events) < cap(o.events) {
		o.events <- OrchestratorEvent{Type: EventWorkerUpdate}
	}
	type result struct {
		sum workspace.DiffSummary
		fd  workspace.FileDiff
		err error
	}
	done := make(chan result, 1)
	go func() {
		sum, err := o.WorktreeDiff(context.Background(), "ENG-1", "")
		if err != nil {
			done <- result{err: err}
			return
		}
		fd, err := o.WorktreeFileDiff(context.Background(), "ENG-1", "", "a.txt")
		done <- result{sum: sum, fd: fd, err: err}
	}()
	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.NotEmpty(t, r.sum.Files)
		assert.NotEmpty(t, r.fd.Patch)
	case <-time.After(10 * time.Second):
		t.Fatal("the diff waited on the event loop")
	}
	assert.Equal(t, cap(o.events), len(o.events), "the diff sent nothing to the event loop")
	for len(o.events) > 0 {
		ev := <-o.events
		assert.Equal(t, EventWorkerUpdate, ev.Type)
	}
}
