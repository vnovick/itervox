package outbox_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/outbox"
)

func mustNew(t *testing.T, path string) *outbox.Outbox {
	t.Helper()
	o, err := outbox.New(path)
	require.NoError(t, err)
	require.NotNil(t, o)
	return o
}

func TestDue_PerIssueFIFOHeadOnly(t *testing.T) {
	dir := t.TempDir()
	o := mustNew(t, filepath.Join(dir, "outbox.json"))

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	o.SetNow(func() time.Time { return base })

	// issue A gets two entries (transition then comment); issue B gets one.
	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"}))
	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindCreateComment, IssueID: "A", Identifier: "ENG-1", Body: "second"}))
	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "B", Identifier: "ENG-2", TargetState: "Done"}))

	due := o.Due(base)
	require.Len(t, due, 2, "Due must return only the head of each issue's FIFO, not every pending entry")

	byIssue := map[string]outbox.Entry{}
	for _, e := range due {
		byIssue[e.IssueID] = e
	}
	require.Contains(t, byIssue, "A")
	require.Contains(t, byIssue, "B")
	assert.Equal(t, outbox.KindUpdateState, byIssue["A"].Kind, "must be the FIRST A entry, not the queued comment")
	assert.Equal(t, "Done", byIssue["A"].TargetState)

	// Flushing issue A's head must expose the next A entry as due.
	o.MarkFlushed(byIssue["A"].ID)
	due2 := o.Due(base)
	require.Len(t, due2, 2)
	var gotSecond bool
	for _, e := range due2 {
		if e.IssueID == "A" {
			assert.Equal(t, outbox.KindCreateComment, e.Kind)
			gotSecond = true
		}
	}
	assert.True(t, gotSecond, "flushing the head must expose the next queued entry for that issue")
}

func TestDue_CrossIssueIndependence(t *testing.T) {
	dir := t.TempDir()
	o := mustNew(t, filepath.Join(dir, "outbox.json"))

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	o.SetNow(func() time.Time { return base })

	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"}))
	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "B", Identifier: "ENG-2", TargetState: "Done"}))

	due := o.Due(base)
	require.Len(t, due, 2)
	var idA string
	for _, e := range due {
		if e.IssueID == "A" {
			idA = e.ID
		}
	}
	require.NotEmpty(t, idA)

	// Fail A: it backs off into the future. B must remain unaffected and due.
	o.MarkFailed(idA, errors.New("boom"), base)
	due2 := o.Due(base)
	require.Len(t, due2, 1)
	assert.Equal(t, "B", due2[0].IssueID)

	// Once A's backoff elapses it becomes due again, independent of B.
	due3 := o.Due(base.Add(11 * time.Second))
	require.Len(t, due3, 2)
}

func TestNew_RestoresPersistedStateAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outbox.json")
	o1 := mustNew(t, path)

	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	o1.SetNow(func() time.Time { return base })

	require.NoError(t, o1.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"}))
	snap := o1.Snapshot()
	require.Len(t, snap, 1)
	id := snap[0].ID

	o1.MarkFailed(id, errors.New("rate limited"), base)

	// "Restart": brand-new Outbox pointed at the same path.
	o2 := mustNew(t, path)
	restored := o2.Snapshot()
	require.Len(t, restored, 1)
	assert.Equal(t, id, restored[0].ID)
	assert.Equal(t, "A", restored[0].IssueID)
	assert.Equal(t, "Done", restored[0].TargetState)
	assert.Equal(t, 1, restored[0].Attempts)
	assert.Equal(t, "rate limited", restored[0].LastError)
	assert.Equal(t, base.Add(10*time.Second), restored[0].NextAttemptAt)
}

func TestNew_CorruptFileWarnsAndStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outbox.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o600))

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	o, err := outbox.New(path)
	require.NoError(t, err)
	require.NotNil(t, o)
	assert.Empty(t, o.Snapshot())
	assert.Contains(t, buf.String(), "level=WARN")
	assert.Contains(t, buf.String(), "outbox")
}

func TestNew_MissingFileStartsEmptyWithoutWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outbox.json") // never created

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	o, err := outbox.New(path)
	require.NoError(t, err)
	assert.Empty(t, o.Snapshot())
	assert.Empty(t, buf.String(), "a missing file is not corruption and must not warn")
}

// TestEnqueue_RollsBackOnPersistFailure exercises Enqueue's atomic
// contract: a failed persist must leave no trace, not even a phantom entry
// Due() would try to deliver.
//
// Seam choice: rather than adding a test-only persist-failure hook to
// export_test.go, this makes the outbox file's parent directory
// unwritable (chmod 0o500) so the real atomicfs.WriteFile call fails for
// real — the exact same technique internal/atomicfs's own
// TestWriteFile_NoPartialOnTempFailure uses. This is the cleaner seam: it
// proves the rollback works against the actual persistence failure mode in
// production code, instead of a synthetic hook that could drift from what
// atomicfs really does on failure.
func TestEnqueue_RollsBackOnPersistFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission simulation isn't reliable on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "outbox.json")
	o := mustNew(t, path)

	require.NoError(t, os.Chmod(dir, 0o500)) // read+execute only: no new files
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"})
	require.Error(t, err, "Enqueue must surface the persist failure")

	assert.Empty(t, o.Due(time.Now()), "a failed Enqueue must not leave an entry Due() can deliver")
	assert.Empty(t, o.Snapshot(), "a failed Enqueue must not leave an entry visible to Snapshot")

	require.NoError(t, os.Chmod(dir, 0o700)) // restore so New can read the (nonexistent) file
	o2 := mustNew(t, path)
	assert.Empty(t, o2.Snapshot(), "a failed Enqueue must not have persisted anything for a restart to restore")
}

func TestEnqueue_ValidatesEntry(t *testing.T) {
	tests := []struct {
		name string
		e    outbox.Entry
	}{
		{
			name: "unknown kind",
			e:    outbox.Entry{Kind: outbox.EntryKind("bogus"), IssueID: "A", Identifier: "ENG-1", TargetState: "Done"},
		},
		{
			name: "empty issue id",
			e:    outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "", Identifier: "ENG-1", TargetState: "Done"},
		},
		{
			name: "empty identifier",
			e:    outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "", TargetState: "Done"},
		},
		{
			name: "update_state missing target state",
			e:    outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: ""},
		},
		{
			name: "create_comment missing body",
			e:    outbox.Entry{Kind: outbox.KindCreateComment, IssueID: "A", Identifier: "ENG-1", Body: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			o := mustNew(t, filepath.Join(dir, "outbox.json"))

			err := o.Enqueue(tt.e)
			require.Error(t, err, "invalid entry must be rejected")
			assert.Empty(t, o.Snapshot(), "an invalid entry must never be stored")
		})
	}

	// Sanity: the valid counterparts of each rejected shape must succeed,
	// proving the table above fails for the stated reason and not some
	// unrelated validation bug.
	t.Run("valid update_state accepted", func(t *testing.T) {
		dir := t.TempDir()
		o := mustNew(t, filepath.Join(dir, "outbox.json"))
		require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"}))
		assert.Len(t, o.Snapshot(), 1)
	})
	t.Run("valid create_comment accepted", func(t *testing.T) {
		dir := t.TempDir()
		o := mustNew(t, filepath.Join(dir, "outbox.json"))
		require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindCreateComment, IssueID: "A", Identifier: "ENG-1", Body: "hi"}))
		assert.Len(t, o.Snapshot(), 1)
	})
}

func TestMarkFailed_BackoffSchedule(t *testing.T) {
	wantDelays := []time.Duration{
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		80 * time.Second,
		160 * time.Second,
		300 * time.Second,
		300 * time.Second,
	}

	dir := t.TempDir()
	o := mustNew(t, filepath.Join(dir, "outbox.json"))
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	o.SetNow(func() time.Time { return base })
	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"}))
	id := o.Snapshot()[0].ID

	for i, want := range wantDelays {
		o.MarkFailed(id, errors.New("fail"), base)
		got := o.Snapshot()[0]
		assert.Equal(t, i+1, got.Attempts, "attempt #%d", i+1)
		assert.Equal(t, base.Add(want), got.NextAttemptAt, "attempt #%d backoff delay", i+1)
	}
}

func TestEntry_DegradedAtFiveAttempts(t *testing.T) {
	dir := t.TempDir()
	o := mustNew(t, filepath.Join(dir, "outbox.json"))
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	o.SetNow(func() time.Time { return base })
	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"}))
	id := o.Snapshot()[0].ID

	for i := 1; i <= 4; i++ {
		o.MarkFailed(id, errors.New("fail"), base)
		assert.False(t, o.Snapshot()[0].Degraded(), "must not be degraded before 5 attempts (attempt %d)", i)
	}
	o.MarkFailed(id, errors.New("fail"), base)
	assert.True(t, o.Snapshot()[0].Degraded(), "must be degraded at attempt 5")

	// No terminal give-up: the entry is still present and keeps retrying.
	o.MarkFailed(id, errors.New("fail"), base)
	got := o.Snapshot()
	require.Len(t, got, 1)
	assert.Equal(t, 6, got[0].Attempts)
	assert.True(t, got[0].Degraded())
}

func TestRetryNow(t *testing.T) {
	dir := t.TempDir()
	o := mustNew(t, filepath.Join(dir, "outbox.json"))
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	o.SetNow(func() time.Time { return base })
	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"}))
	id := o.Snapshot()[0].ID

	o.MarkFailed(id, errors.New("fail"), base) // NextAttemptAt -> base+10s
	assert.Empty(t, o.Due(base), "entry must not be due while backed off")

	laterNow := base.Add(2 * time.Second)
	o.SetNow(func() time.Time { return laterNow })
	assert.True(t, o.RetryNow(id))
	assert.Equal(t, laterNow, o.Snapshot()[0].NextAttemptAt)
	assert.Len(t, o.Due(laterNow), 1, "RetryNow must make the entry immediately due")

	assert.False(t, o.RetryNow("does-not-exist"), "RetryNow only operates on an existing id")
}

func TestDrop_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	o := mustNew(t, filepath.Join(dir, "outbox.json"))
	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"}))
	id := o.Snapshot()[0].ID

	o.Drop(id, "already_applied")
	assert.Empty(t, o.Snapshot())

	assert.NotPanics(t, func() { o.Drop(id, "already_applied") }, "dropping an already-dropped id must not panic or error")
	assert.NotPanics(t, func() { o.Drop("never-existed", "operator") }, "dropping an unknown id must not panic or error")
}

func TestSnapshotAndPendingFor_ReturnRealCopies(t *testing.T) {
	dir := t.TempDir()
	o := mustNew(t, filepath.Join(dir, "outbox.json"))
	require.NoError(t, o.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "A", Identifier: "ENG-1", TargetState: "Done"}))

	snap := o.Snapshot()
	require.Len(t, snap, 1)
	snap[0].TargetState = "MUTATED"
	snap[0].Attempts = 999

	snap2 := o.Snapshot()
	require.Len(t, snap2, 1)
	assert.Equal(t, "Done", snap2[0].TargetState, "mutating a Snapshot copy must not affect internal state")
	assert.Equal(t, 0, snap2[0].Attempts)

	pending := o.PendingFor("A")
	require.Len(t, pending, 1)
	pending[0].TargetState = "ALSO MUTATED"

	pending2 := o.PendingFor("A")
	require.Len(t, pending2, 1)
	assert.Equal(t, "Done", pending2[0].TargetState, "mutating a PendingFor copy must not affect internal state")
}

func TestConcurrentEnqueueDueMarkFlushedMarkFailed(t *testing.T) {
	dir := t.TempDir()
	o := mustNew(t, filepath.Join(dir, "outbox.json"))

	const issues = 8
	const perIssue = 15
	const consumerIters = 200

	var wg sync.WaitGroup

	for i := 0; i < issues; i++ {
		issueID := fmt.Sprintf("ISSUE-%d", i)
		wg.Add(1)
		go func(issueID string) {
			defer wg.Done()
			for j := 0; j < perIssue; j++ {
				_ = o.Enqueue(outbox.Entry{
					Kind:        outbox.KindUpdateState,
					IssueID:     issueID,
					Identifier:  issueID,
					TargetState: "Done",
				})
			}
		}(issueID)
	}

	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < consumerIters; j++ {
				now := time.Now()
				due := o.Due(now)
				for _, e := range due {
					if e.Attempts%2 == 0 {
						o.MarkFailed(e.ID, errors.New("transient"), now)
					} else {
						o.MarkFlushed(e.ID)
					}
				}
				_ = o.Snapshot()
				_ = o.PendingFor("ISSUE-0")
				_ = o.RetryNow(fmt.Sprintf("ISSUE-%d", j%issues)) // exercises the not-found path concurrently
			}
		}()
	}

	wg.Wait()

	final := o.Snapshot()
	byIssue := map[string][]outbox.Entry{}
	for _, e := range final {
		assert.NotEmpty(t, e.ID)
		assert.NotEmpty(t, e.IssueID)
		byIssue[e.IssueID] = append(byIssue[e.IssueID], e)
	}

	// Per-issue FIFO invariant: surviving entries for an issue must still
	// appear in EnqueuedAt order — MarkFlushed/MarkFailed only ever remove
	// or update in place, never reorder.
	for issueID, entries := range byIssue {
		for i := 1; i < len(entries); i++ {
			assert.False(t, entries[i].EnqueuedAt.Before(entries[i-1].EnqueuedAt),
				"issue %s: surviving entries must remain in EnqueuedAt (FIFO) order", issueID)
		}
	}

	// Head-only invariant: even after the storm, Due() returns at most one
	// entry per issue, and it must be that issue's earliest surviving entry.
	farFuture := time.Now().Add(time.Hour)
	due := o.Due(farFuture)
	dueByIssue := map[string]outbox.Entry{}
	for _, e := range due {
		_, dup := dueByIssue[e.IssueID]
		require.False(t, dup, "Due must return at most one entry per issue")
		dueByIssue[e.IssueID] = e
	}
	for issueID, entries := range byIssue {
		require.NotEmpty(t, entries)
		got, ok := dueByIssue[issueID]
		require.True(t, ok, "issue %s must have a due head entry", issueID)
		assert.Equal(t, entries[0].ID, got.ID, "Due must return the FIFO head, not any later entry")
	}
}

// TestNew_DropsInvalidPersistedEntries pins that the load path enforces the
// same invariants Enqueue does. validateEntry used to guard only Enqueue,
// which left it as the *assumed* sole door into o.entries — but New is a
// second door. A persisted entry with an unknown Kind (a hand edit, a
// rolled-back schema, or a downgrade from a build that added a kind) parses
// as valid JSON and used to load clean. Because Due is per-issue-FIFO
// head-only and the flusher cannot deliver an unknown kind, such an entry
// pinned its issue's entire write queue forever while reporting Attempts=0
// and Degraded()=false — a silent, permanent, invisible write loss.
func TestNew_DropsInvalidPersistedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	require.NoError(t, os.WriteFile(path, []byte(`[
	  {"id":"poison","kind":"move_issue","issue_id":"ISSUE-1","identifier":"ENG-1",
	   "enqueued_at":"2026-01-01T00:00:00Z","attempts":0,"next_attempt_at":"2026-01-01T00:00:00Z"},
	  {"id":"real","kind":"update_state","issue_id":"ISSUE-1","identifier":"ENG-1",
	   "target_state":"Done","enqueued_at":"2026-01-02T00:00:00Z","attempts":0,
	   "next_attempt_at":"2026-01-02T00:00:00Z"}
	]`), 0o644))

	ob, err := outbox.New(path)
	require.NoError(t, err)

	due := ob.Due(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	require.Len(t, due, 1, "the surviving legitimate entry must be deliverable")
	assert.Equal(t, "real", due[0].ID, "the poison entry must not hold the FIFO head")
	assert.Equal(t, outbox.KindUpdateState, due[0].Kind)
}

// TestNew_KeepsValidPersistedEntriesUntouched guards the drop above from
// over-reaching: a well-formed file must survive load byte-for-byte.
func TestNew_KeepsValidPersistedEntriesUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	first, err := outbox.New(path)
	require.NoError(t, err)
	require.NoError(t, first.Enqueue(outbox.Entry{
		Kind: outbox.KindUpdateState, IssueID: "ISSUE-1", Identifier: "ENG-1",
		TargetState: "Done", FromState: "In Progress",
	}))
	require.NoError(t, first.Enqueue(outbox.Entry{
		Kind: outbox.KindCreateComment, IssueID: "ISSUE-2", Identifier: "ENG-2", Body: "hi",
	}))

	reloaded, err := outbox.New(path)
	require.NoError(t, err)
	got := reloaded.Snapshot()
	require.Len(t, got, 2)
	assert.Equal(t, "In Progress", got[0].FromState, "FromState must survive a restart")
	assert.Equal(t, outbox.KindCreateComment, got[1].Kind)
}
