package local

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
)

func newTestTracker(t *testing.T) (*Tracker, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "issues")
	tr := New(Config{Dir: dir, ActiveStates: []string{"Todo", "In Progress"}, TerminalStates: []string{"Done"}})
	tr.now = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }
	return tr, dir
}

// writeRaw writes a file and moves its mtime forward so the stat-based
// rescan sees the change even within one filesystem timestamp tick.
func writeRaw(t *testing.T, path, content string) {
	t.Helper()
	var next time.Time
	if info, err := os.Stat(path); err == nil {
		next = info.ModTime().Add(2 * time.Second)
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	if !next.IsZero() {
		require.NoError(t, os.Chtimes(path, next, next))
	}
}

// TestLocalTrackerCRUD (#85): create, read, comment, state change and
// branch, and every change survives a new tracker over the same directory.
func TestLocalTrackerCRUD(t *testing.T) {
	ctx := context.Background()
	tr, dir := newTestTracker(t)

	created, err := tr.CreateIssue(ctx, "", "Add a --json flag", "Print the report as JSON.", "Todo")
	require.NoError(t, err)
	assert.Equal(t, "ITX-1", created.Identifier)
	assert.Equal(t, "ITX-1", created.ID)
	second, err := tr.CreateIssue(ctx, "", "Second", "", "Backlog")
	require.NoError(t, err)
	assert.Equal(t, "ITX-2", second.Identifier)
	_, err = tr.CreateIssue(ctx, "", "  ", "", "Todo")
	require.Error(t, err, "a title is required")

	require.NoError(t, tr.UpdateIssueState(ctx, "ITX-1", "In Progress"))
	require.NoError(t, tr.SetIssueBranch(ctx, "ITX-1", "itervox/itx-1"))
	c, err := tr.CreateComment(ctx, "ITX-1", "Started.\n\nWorking on it.")
	require.NoError(t, err)
	assert.Equal(t, "ITX-1-c1", c.ID)
	assert.Equal(t, ItervoxAuthor, c.AuthorName)

	cands, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, cands, 1, "only ITX-1 is in an active state")
	assert.Equal(t, "ITX-1", cands[0].Identifier)

	// A fresh tracker (a restart) reads everything back from the files.
	again := New(Config{Dir: dir, ActiveStates: []string{"Todo", "In Progress"}})
	is, err := again.FetchIssueDetail(ctx, "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "Add a --json flag", is.Title)
	assert.Equal(t, "In Progress", is.State)
	require.NotNil(t, is.Description)
	assert.Equal(t, "Print the report as JSON.", *is.Description)
	require.NotNil(t, is.BranchName)
	assert.Equal(t, "itervox/itx-1", *is.BranchName)
	require.Len(t, is.Comments, 1)
	assert.Equal(t, "Started.\n\nWorking on it.", is.Comments[0].Body)
	assert.Equal(t, ItervoxAuthor, is.Comments[0].AuthorName)
	require.NotNil(t, is.UpdatedAt)

	byState, err := again.FetchIssuesByStates(ctx, []string{"backlog"})
	require.NoError(t, err)
	require.Len(t, byState, 1, "state matching ignores case")
	assert.Equal(t, "ITX-2", byState[0].Identifier)

	byID, err := again.FetchIssueStatesByIDs(ctx, []string{"ITX-2", "ITX-99"})
	require.NoError(t, err)
	require.Len(t, byID, 1)

	_, err = again.FetchIssueDetail(ctx, "ITX-99")
	var nf *tracker.NotFoundError
	require.ErrorAs(t, err, &nf)
	require.ErrorAs(t, again.UpdateIssueState(ctx, "ITX-99", "Done"), &nf)

	// Idempotent comments.
	_, found, err := again.FindCommentByKey(ctx, "ITX-1", "k1")
	require.NoError(t, err)
	assert.False(t, found)
	_, err = again.CreateCommentWithKey(ctx, "ITX-1", "k1", "Keyed")
	require.NoError(t, err)
	got, found, err := again.FindCommentByKey(ctx, "ITX-1", "k1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "ITX-1-c2", got.ID)

	// Numbering continues after a restart.
	third, err := again.CreateIssue(ctx, "", "Third", "", "Todo")
	require.NoError(t, err)
	assert.Equal(t, "ITX-3", third.Identifier)
}

// TestLocalTrackerBlockers (#85): blockers resolve their state and branch
// from the other files; a blocker with no file has no state, so it keeps the
// dependent blocked.
func TestLocalTrackerBlockers(t *testing.T) {
	ctx := context.Background()
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: Base\nstate: In Review\nbranch: itervox/itx-1\n---\n")
	writeRaw(t, filepath.Join(dir, "ITX-2.md"), "---\ntitle: Depends\nstate: Todo\nblocked_by: [ITX-1, ITX-9]\n---\n")

	is, err := tr.FetchIssueDetail(ctx, "ITX-2")
	require.NoError(t, err)
	require.Len(t, is.BlockedBy, 2)
	b := is.BlockedBy[0]
	require.NotNil(t, b.Identifier)
	assert.Equal(t, "ITX-1", *b.Identifier)
	require.NotNil(t, b.State)
	assert.Equal(t, "In Review", *b.State)
	require.NotNil(t, b.BranchName)
	assert.Equal(t, "itervox/itx-1", *b.BranchName)
	assert.Nil(t, is.BlockedBy[1].State, "a missing blocker has no state")

	// Moving the blocker is reflected on the dependent.
	require.NoError(t, tr.UpdateIssueState(ctx, "ITX-1", "Done"))
	is, err = tr.FetchIssueDetail(ctx, "ITX-2")
	require.NoError(t, err)
	assert.Equal(t, "Done", *is.BlockedBy[0].State)

	// A single value is a one-item list.
	writeRaw(t, filepath.Join(dir, "ITX-3.md"), "---\ntitle: One\nstate: Todo\nblocked_by: ITX-1\n---\n")
	is, err = tr.FetchIssueDetail(ctx, "ITX-3")
	require.NoError(t, err)
	require.Len(t, is.BlockedBy, 1)
}

// TestLocalTrackerPicksUpManualEdits (#85): edits made outside Itervox
// appear on the next call, without a restart; new and deleted files too.
func TestLocalTrackerPicksUpManualEdits(t *testing.T) {
	ctx := context.Background()
	tr, dir := newTestTracker(t)
	p := filepath.Join(dir, "ITX-1.md")
	writeRaw(t, p, "---\ntitle: First\nstate: Backlog\n---\n")
	cands, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.Empty(t, cands)

	writeRaw(t, p, "---\ntitle: First, edited\nstate: Todo\n---\n\nNow ready.\n")
	cands, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "First, edited", cands[0].Title)
	assert.Equal(t, "Now ready.", *cands[0].Description)

	writeRaw(t, filepath.Join(dir, "ITX-2.md"), "---\ntitle: New\nstate: Todo\n---\n")
	cands, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, cands, 2)

	require.NoError(t, os.Remove(p))
	cands, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "ITX-2", cands[0].Identifier)

	// Files that are not issues are ignored.
	writeRaw(t, filepath.Join(dir, "README.md"), "notes")
	writeRaw(t, filepath.Join(dir, ".ITX-5.md.tmp-1"), "partial")
	cands, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Empty(t, tr.Problems())
}

// TestLocalTrackerMalformedFile (#85): a file that does not parse is
// reported, its last good version keeps being served, Itervox refuses to
// overwrite it, and fixing it clears the report. It is never fatal.
func TestLocalTrackerMalformedFile(t *testing.T) {
	ctx := context.Background()
	tr, dir := newTestTracker(t)
	p := filepath.Join(dir, "ITX-1.md")
	writeRaw(t, p, "---\ntitle: Good\nstate: Todo\n---\n")
	writeRaw(t, filepath.Join(dir, "ITX-2.md"), "no front matter")
	cands, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err, "a malformed file is not fatal")
	require.Len(t, cands, 1)
	probs := tr.Problems()
	require.Len(t, probs, 1)
	assert.Equal(t, filepath.Join(dir, "ITX-2.md"), probs[0].Path)
	assert.Contains(t, probs[0].Err, "front matter")

	// Breaking a known-good file keeps serving the last good version.
	writeRaw(t, p, "---\ntitle: [unclosed\nstate: Todo\n---\n")
	is, err := tr.FetchIssueDetail(ctx, "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "Good", is.Title)
	assert.Len(t, tr.Problems(), 2)
	err = tr.UpdateIssueState(ctx, "ITX-1", "Done")
	require.Error(t, err, "Itervox never overwrites a broken file")
	assert.Contains(t, err.Error(), "does not parse")
	data, _ := os.ReadFile(p)
	assert.Contains(t, string(data), "[unclosed", "the operator's edit is untouched")

	// Fixing it clears the problem.
	writeRaw(t, p, "---\ntitle: Fixed\nstate: Todo\n---\n")
	is, err = tr.FetchIssueDetail(ctx, "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "Fixed", is.Title)
	assert.Len(t, tr.Problems(), 1)

	for _, bad := range []string{
		"---\nstate: Todo\n---\n",                               // no title
		"---\ntitle: x\n---\n",                                  // no state
		"---\ntitle: x\nstate: Todo\npriority: high\n---\n",     // priority not a number
		"---\ntitle: x\nstate: Todo\ncreated: yesterday\n---\n", // bad time
		"---\ntitle: x\nstate: Todo\n",                          // not closed
	} {
		_, err := parseIssueFile([]byte(bad))
		assert.Error(t, err, bad)
	}
}

// TestLocalIssueFileRoundTrip (#85): the file format round-trips, keeps
// unknown front matter keys, and does not mistake a quoted comments heading
// inside a code fence for the comment section.
func TestLocalIssueFileRoundTrip(t *testing.T) {
	src := "---\ntitle: 'Fix: the parser'\nstate: Todo\npriority: 2\nlabels: [cli, Bug]\nblocked_by: [ITX-3]\ncreated: 2026-10-09T12:00:00Z\nestimate: 3\n---\n\n" +
		"Description.\n\n```md\n## Comments\n### 2026-01-01T00:00:00Z — nobody\n```\n\n## Comments\n\n### 2026-10-09T12:05:00Z — alex\n\nFirst.\n\n### 2026-10-09T12:06:00Z — Itervox\n\nSecond.\n"
	f, err := parseIssueFile([]byte(src))
	require.NoError(t, err)
	assert.Equal(t, "Fix: the parser", f.Title)
	require.NotNil(t, f.Priority)
	assert.Equal(t, 2, *f.Priority)
	assert.Equal(t, []string{"cli", "Bug"}, f.Labels)
	assert.Equal(t, 3, f.Extra["estimate"])
	assert.Contains(t, f.Body, "```md\n## Comments", "a heading with no comment after it stays in the description")
	require.Len(t, f.Comments, 2)
	assert.Equal(t, "alex", f.Comments[0].Author)
	assert.Equal(t, "First.", f.Comments[0].Body)

	again, err := parseIssueFile(f.render())
	require.NoError(t, err)
	assert.Equal(t, f.Title, again.Title)
	assert.Equal(t, f.Body, again.Body)
	assert.Equal(t, f.Comments, again.Comments)
	assert.Equal(t, f.Extra, again.Extra)
	assert.Equal(t, f.Labels, again.Labels)
	assert.Equal(t, f.BlockedBy, again.BlockedBy)

	is := f.toIssue("ITX-4")
	assert.Equal(t, []string{"cli", "bug"}, is.Labels, "labels are lower-cased like the other trackers")
	assert.True(t, strings.HasPrefix(is.Comments[1].ID, "ITX-4-c"))
}

// TestWriteIssueRefusesToOverwrite (#85): seeding never clobbers a file.
func TestWriteIssueRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteIssue(dir, "ITX-1", IssueSpec{Title: "Example", State: "Backlog"}))
	assert.Error(t, WriteIssue(dir, "ITX-1", IssueSpec{Title: "Other", State: "Todo"}))
	assert.Error(t, WriteIssue(dir, "not an id", IssueSpec{Title: "x", State: "Todo"}))
	tr := New(Config{Dir: dir})
	is, err := tr.FetchIssueDetail(context.Background(), "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "Example", is.Title)
}

// TestLocalIssueCommentsCannotChangeTheFileStructure (#85): comment text
// that looks like the comments heading or a comment header, or leaves a
// code fence open, reads back exactly after a restart and never moves text
// into the description or invents a comment.
func TestLocalIssueCommentsCannotChangeTheFileStructure(t *testing.T) {
	ctx := context.Background()
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: T\nstate: Todo\n---\n\nDesc.\n")
	bodies := []string{
		"first",
		"Summary\n\n## Comments\n\nnone",
		"log:\n### 2026-01-01T00:00:00Z — bot\nline",
		"literal \\## Comments and\n\\## Comments\n\\\\### 2026-01-01T00:00:00Z — x",
		"```\nunclosed fence\n### 2026-01-01T00:00:00Z — hidden",
		"last",
	}
	for _, b := range bodies {
		_, err := tr.CreateComment(ctx, "ITX-1", b)
		require.NoError(t, err)
	}
	restarted := New(Config{Dir: dir})
	is, err := restarted.FetchIssueDetail(ctx, "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "Desc.", *is.Description)
	require.Len(t, is.Comments, len(bodies))
	for i, b := range bodies {
		assert.Equal(t, b, is.Comments[i].Body, "comment %d", i)
		assert.Equal(t, ItervoxAuthor, is.Comments[i].AuthorName)
	}

	// A new issue whose body looks like a comment section keeps it as the
	// description.
	created, err := restarted.CreateIssue(ctx, "", "Spec", "Intro\n\n## Comments\n\n### 2026-01-01T00:00:00Z — x\n\ny", "Todo")
	require.NoError(t, err)
	got, err := New(Config{Dir: dir}).FetchIssueDetail(ctx, created.Identifier)
	require.NoError(t, err)
	assert.Empty(t, got.Comments)
	assert.Equal(t, "Intro\n\n## Comments\n\n### 2026-01-01T00:00:00Z — x\n\ny", *got.Description,
		"read back exactly, without the escapes")
}

// TestLocalIssueCodeFencesNeverHideComments (#85): Markdown is not parsed to
// find the comment section, so a description or comment with an unclosed
// fence, a longer fence or inline triple-backtick code keeps every comment
// Itervox writes a comment, and keyed comments are found.
func TestLocalIssueCodeFencesNeverHideComments(t *testing.T) {
	ctx := context.Background()
	for name, desc := range map[string]string{
		"unclosed fence":      "Steps:\n```go\nfunc x()",
		"four-backtick fence": "````md\n```\n````\nafter",
		"inline code":         "```make test``` must pass.",
	} {
		t.Run(name, func(t *testing.T) {
			tr, dir := newTestTracker(t)
			is, err := tr.CreateIssue(ctx, "", "T", desc, "Todo")
			require.NoError(t, err)
			_, err = tr.CreateComment(ctx, is.ID, "```unbalanced")
			require.NoError(t, err)
			_, err = tr.CreateCommentWithKey(ctx, is.ID, "k1", "keyed")
			require.NoError(t, err)
			_, err = tr.CreateComment(ctx, is.ID, "c3")
			require.NoError(t, err)
			restarted := New(Config{Dir: dir})
			got, err := restarted.FetchIssueDetail(ctx, is.ID)
			require.NoError(t, err)
			assert.Equal(t, desc, *got.Description)
			require.Len(t, got.Comments, 3)
			assert.Equal(t, "```unbalanced", got.Comments[0].Body)
			assert.Equal(t, "c3", got.Comments[2].Body)
			_, found, err := restarted.FindCommentByKey(ctx, is.ID, "k1")
			require.NoError(t, err)
			assert.True(t, found)
		})
	}

	// Hand-written comments: inline code at a line start, and an empty
	// author, still separate the comments.
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: T\nstate: Todo\n---\n\nD.\n\n## Comments\n\n"+
		"### 2026-10-09T12:00:00Z — alex\n\n```make``` fails\n\n### 2026-10-09T12:01:00Z — bo\n\nok\n\n### 2026-10-09T12:02:00Z — \n\nanon\n")
	is, err := tr.FetchIssueDetail(ctx, "ITX-1")
	require.NoError(t, err)
	require.Len(t, is.Comments, 3)
	assert.Equal(t, "```make``` fails", is.Comments[0].Body)
	assert.Equal(t, "bo", is.Comments[1].AuthorName)
	assert.Equal(t, "", is.Comments[2].AuthorName)
	assert.Equal(t, "anon", is.Comments[2].Body)
}

// TestLocalIssueKeepsTextUnderTheCommentsHeading (#85): a `## Comments`
// heading with no comment after it is description, and text between the
// heading and the first comment is kept when Itervox rewrites the file.
func TestLocalIssueKeepsTextUnderTheCommentsHeading(t *testing.T) {
	ctx := context.Background()
	tr, dir := newTestTracker(t)
	writeRaw(t, filepath.Join(dir, "ITX-1.md"),
		"---\ntitle: T\nstate: Todo\n---\n\nIntro.\n\n## Comments\n\nPlease comment on the PR, not here.\n\nMore spec text.\n")
	require.NoError(t, tr.UpdateIssueState(ctx, "ITX-1", "In Progress"))
	is, err := New(Config{Dir: dir}).FetchIssueDetail(ctx, "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "Intro.\n\n## Comments\n\nPlease comment on the PR, not here.\n\nMore spec text.", *is.Description)

	writeRaw(t, filepath.Join(dir, "ITX-2.md"),
		"---\ntitle: T\nstate: Todo\n---\n\nD.\n\n## Comments\n\nOperator note without a header.\n\n### 2026-10-09T12:00:00Z — alex\n\nHi.\n")
	_, err = tr.CreateComment(ctx, "ITX-2", "Reply.")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(dir, "ITX-2.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "Operator note without a header.")
	is, err = New(Config{Dir: dir}).FetchIssueDetail(ctx, "ITX-2")
	require.NoError(t, err)
	require.Len(t, is.Comments, 2)
	assert.Equal(t, "alex", is.Comments[0].AuthorName)
	assert.Equal(t, "D.", *is.Description)
}

// TestLocalTrackerNeverOverwritesAnUnseenEdit (#85): an edit the scan cannot
// see (same size, modification time restored) is still read before Itervox
// writes the file, so it is kept, and an edit inside the same timestamp tick
// as the last read is picked up on the next call.
func TestLocalTrackerNeverOverwritesAnUnseenEdit(t *testing.T) {
	ctx := context.Background()
	tr, dir := newTestTracker(t)
	p := filepath.Join(dir, "ITX-1.md")
	writeRaw(t, p, "---\ntitle: T\nstate: Todo\n---\n")
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(p, old, old))
	is, err := tr.FetchIssueDetail(ctx, "ITX-1")
	require.NoError(t, err)
	require.Equal(t, "Todo", is.State)

	require.NoError(t, os.WriteFile(p, []byte("---\ntitle: T\nstate: Done\n---\n"), 0o644)) // same size
	require.NoError(t, os.Chtimes(p, old, old))
	require.NoError(t, tr.SetIssueBranch(ctx, "ITX-1", "itervox/itx-1"))
	is, err = tr.FetchIssueDetail(ctx, "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "Done", is.State, "the operator's edit was kept")
	require.NotNil(t, is.BranchName)

	// Same size and the same (recent) mtime as the read: read again.
	p2 := filepath.Join(dir, "ITX-2.md")
	now := time.Now()
	require.NoError(t, os.WriteFile(p2, []byte("---\ntitle: T\nstate: Todo\n---\n"), 0o644))
	require.NoError(t, os.Chtimes(p2, now, now))
	_, err = tr.FetchIssueDetail(ctx, "ITX-2")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p2, []byte("---\ntitle: T\nstate: Done\n---\n"), 0o644))
	require.NoError(t, os.Chtimes(p2, now, now))
	is, err = tr.FetchIssueDetail(ctx, "ITX-2")
	require.NoError(t, err)
	assert.Equal(t, "Done", is.State)
}
