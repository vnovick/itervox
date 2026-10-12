package store_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// #114 — incremental pulls.

// allStates is every state the changes fake knows.
var allStates = []string{"Backlog", "Todo", "In Progress", "Done", "Triage"}

// changesTracker is a remote adapter that can list what changed since a
// time: every edit stamps the issue's update time (the tracker's clock), and
// a removed issue is listed as gone.
type changesTracker struct {
	*countingTracker
	mu      sync.Mutex
	stamps  map[string]time.Time
	titles  map[string]string
	removed map[string]time.Time
	clock   time.Time
	since   []time.Time
	inc     atomic.Int64
	// afterList and afterInc, when set, run after a FetchIssuesByStates or
	// FetchIssuesUpdatedSince listing is read and before it is returned: an
	// edit made while a pull is in flight.
	afterList func()
	afterInc  func()
}

func newChanges(issues ...domain.Issue) *changesTracker {
	c := &changesTracker{
		countingTracker: newCounting(issues...),
		stamps:          map[string]time.Time{},
		titles:          map[string]string{},
		removed:         map[string]time.Time{},
		clock:           time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
	}
	for _, iss := range issues {
		c.stamps[iss.ID] = c.clock
	}
	return c
}

// tick advances the tracker's clock and returns it.
func (c *changesTracker) tick() time.Time {
	c.clock = c.clock.Add(time.Minute)
	return c.clock
}

func (c *changesTracker) setState(id, state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.SetIssueState(id, state)
	c.stamps[id] = c.tick()
}

// setStateAt changes an issue's state with an update time the tracker
// stamped at: before the store's cursor, for a late-committed update.
func (c *changesTracker) setStateAt(id, state string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.SetIssueState(id, state)
	c.stamps[id] = at
}

func (c *changesTracker) setTitle(id, title string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.titles[id] = title
	c.stamps[id] = c.tick()
}

func (c *changesTracker) remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removed[id] = c.tick()
}

// view applies the fake's edits to a memory-tracker result. Caller holds mu.
func (c *changesTracker) view(list []domain.Issue) []domain.Issue {
	out := make([]domain.Issue, 0, len(list))
	for _, iss := range list {
		if _, gone := c.removed[iss.ID]; gone {
			continue
		}
		if at, ok := c.stamps[iss.ID]; ok {
			iss.UpdatedAt = &at
		}
		if title, ok := c.titles[iss.ID]; ok {
			iss.Title = title
		}
		out = append(out, iss)
	}
	return out
}

func (c *changesTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	list, err := c.countingTracker.FetchCandidateIssues(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.view(list), err
}

func (c *changesTracker) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	list, err := c.countingTracker.FetchIssuesByStates(ctx, states)
	c.mu.Lock()
	list = c.view(list)
	after := c.afterList
	c.afterList = nil
	c.mu.Unlock()
	if after != nil {
		after()
	}
	return list, err
}

func (c *changesTracker) FetchIssuesUpdatedSince(ctx context.Context, since time.Time) ([]domain.Issue, []string, error) {
	c.inc.Add(1)
	if c.fail.Load() {
		return nil, nil, errDown
	}
	list, err := c.MemoryTracker.FetchIssuesByStates(ctx, allStates)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	defer func() {
		after := c.afterInc
		c.afterInc = nil
		c.mu.Unlock()
		if after != nil {
			after()
		}
	}()
	c.since = append(c.since, since)
	var changed []domain.Issue
	for _, iss := range c.view(list) {
		if !iss.UpdatedAt.Before(since) {
			changed = append(changed, iss)
		}
	}
	var gone []string
	for id, at := range c.removed {
		if !at.Before(since) {
			gone = append(gone, id)
		}
	}
	return changed, gone, nil
}

func (c *changesTracker) lastSince() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.since[len(c.since)-1]
}

func incrementalStore(t *testing.T, up *changesTracker, dir string) (*store.Store, func() []string) {
	t.Helper()
	cfg := storeConfig(t, dir)
	tr, st, err := store.Wrap(up, cfg)
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context()) // the first, full pull
	require.NoError(t, err)
	return st, func() []string {
		cands, err := tr.FetchCandidateIssues(t.Context())
		require.NoError(t, err)
		return identifiers(cands)
	}
}

// TestQuietIncrementalPullMakesOneRequestAndChangesNothing: after the first
// full pull, a pull with no changes in the tracker is one list request (the
// updated-since listing), no full list request, and leaves the store and its
// file exactly as they were.
func TestQuietIncrementalPullMakesOneRequestAndChangesNothing(t *testing.T) {
	dir := t.TempDir()
	up := newChanges(issue("1", "Todo"), issue("2", "Backlog"), issue("3", "Done"))
	st, cands := incrementalStore(t, up, dir)
	fullCalls := up.listCalls()
	path := filepath.Join(dir, "tracker_store.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	beforeInfo, err := os.Stat(path)
	require.NoError(t, err)

	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, int64(1), up.inc.Load(), "one updated-since request")
	assert.Equal(t, fullCalls, up.listCalls(), "no full list request")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	afterInfo, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the store file is unchanged")
	assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(), "and not rewritten")
	assert.Equal(t, []string{"ENG-1"}, cands())
}

// TestIncrementalPullAppliesTrackerChanges: an issue edited in the tracker
// shows the edit after the next pull; one closed, or moved out of the active
// states, leaves the candidates; one moved into an active state joins them;
// one removed leaves the store; an issue's recorded blocker state follows
// the blocker.
func TestIncrementalPullAppliesTrackerChanges(t *testing.T) {
	blockerID, blockerIdent, todo := "5", "ENG-5", "Todo"
	blocked := issue("6", "Todo")
	blocked.BlockedBy = []domain.BlockerRef{{ID: &blockerID, Identifier: &blockerIdent, State: &todo}}
	up := newChanges(issue("1", "Todo"), issue("2", "In Progress"), issue("3", "Backlog"),
		issue("4", "Todo"), issue("5", "Todo"), blocked)
	st, cands := incrementalStore(t, up, t.TempDir())
	require.ElementsMatch(t, []string{"ENG-1", "ENG-2", "ENG-4", "ENG-5", "ENG-6"}, cands())
	fullCalls := up.listCalls()

	up.setTitle("1", "Renamed in the tracker")
	up.setState("2", "Done")   // closed
	up.setState("3", "Todo")   // moved into an active state
	up.setState("4", "Triage") // moved out of the tracked states
	up.setState("5", "Done")   // the blocker finished
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, fullCalls, up.listCalls(), "an incremental pull")
	assert.ElementsMatch(t, []string{"ENG-1", "ENG-3", "ENG-6"}, cands())

	board, err := storeIssues(t, st)
	require.NoError(t, err)
	assert.Equal(t, "Renamed in the tracker", board["1"].Title, "the edit is in the store")
	assert.Equal(t, "Done", board["2"].State)
	require.Len(t, board["6"].BlockedBy, 1)
	assert.Equal(t, "Done", *board["6"].BlockedBy[0].State, "the blocked issue's blocker state follows the blocker")

	up.remove("1")
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.ElementsMatch(t, []string{"ENG-3", "ENG-6"}, cands(), "a removed issue leaves the store")
	board, err = storeIssues(t, st)
	require.NoError(t, err)
	assert.NotContains(t, board, "1")
}

// storeIssues reads the store's board view (a configured view, so it is
// answered from the store with no tracker call).
func storeIssues(t *testing.T, st *store.Store) (map[string]domain.Issue, error) {
	t.Helper()
	list, err := st.FetchIssuesByStates(t.Context(), board)
	out := map[string]domain.Issue{}
	for _, iss := range list {
		out[iss.ID] = iss
	}
	return out, err
}

// TestIncrementalCursorIsTheTrackersNewestUpdate: the cursor is the newest
// update time the tracker reported, so the next pull asks from there,
// whatever the local clock says; it survives a restart.
func TestIncrementalCursorIsTheTrackersNewestUpdate(t *testing.T) {
	dir := t.TempDir()
	up := newChanges(issue("1", "Todo"))
	st, _ := incrementalStore(t, up, dir)
	first := st.CursorForTest()
	assert.Equal(t, up.clock, first, "after the full pull: the newest update it read")

	up.setState("1", "In Progress")
	edited := up.clock
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, first.Add(-store.CursorOverlap), up.lastSince(), "the pull asked from the previous cursor, less the overlap")
	assert.Equal(t, edited, st.CursorForTest(), "and moved it to the edit")

	// A restarted daemon's first pull is incremental from the saved cursor.
	up2 := newChanges(issue("1", "In Progress"))
	tr2, st2, err := store.Wrap(up2, storeConfig(t, dir))
	require.NoError(t, err)
	_, err = tr2.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	require.NoError(t, st2.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, int64(0), up2.listCalls(), "no full pull after the restart")
	assert.Equal(t, edited.Add(-store.CursorOverlap), up2.lastSince(), "the saved cursor is used")
}

// TestFailedIncrementalPullKeepsTheCursor: a pull that fails changes nothing
// and keeps the cursor, so the retry asks for everything since the last good
// pull; retries back off from RetryBase up to the sync interval.
func TestFailedIncrementalPullKeepsTheCursor(t *testing.T) {
	up := newChanges(issue("1", "Todo"))
	st, cands := incrementalStore(t, up, t.TempDir())
	cursor := st.CursorForTest()

	up.setState("1", "Done")
	up.fail.Store(true)
	require.Error(t, st.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, cursor, st.CursorForTest(), "the cursor is kept")
	_, lastErr := st.Status()
	assert.Error(t, lastErr)

	up.fail.Store(false)
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, cursor.Add(-store.CursorOverlap), up.lastSince(), "the retry asks from the last good cursor")
	assert.Empty(t, cands(), "and applies the change it missed")

	assert.Equal(t, store.RetryBase, store.RetryDelayForTest(1, time.Hour))
	assert.Equal(t, 4*store.RetryBase, store.RetryDelayForTest(3, time.Hour))
	assert.Equal(t, time.Minute, store.RetryDelayForTest(20, time.Minute), "capped at the sync interval")
}

// TestFullPullRunsEveryFullSyncInterval: once FullSyncInterval has passed
// since the last full pull, the next pull is full again.
func TestFullPullRunsEveryFullSyncInterval(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	up := newChanges(issue("1", "Todo"))
	cfg := storeConfig(t, t.TempDir())
	cfg.FullSyncInterval = 30 * time.Minute
	cfg.Now = func() time.Time { return now }
	tr, st, err := store.Wrap(up, cfg)
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	full := up.listCalls()

	now = now.Add(20 * time.Minute)
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, full, up.listCalls(), "within the interval: incremental")
	now = now.Add(20 * time.Minute)
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Greater(t, up.listCalls(), full, "past it: a full pull")
	assert.Equal(t, int64(1), up.inc.Load())
}

// TestAdapterWithoutIncrementalAlwaysPullsFull: an adapter that cannot list
// changes keeps the full pull.
func TestAdapterWithoutIncrementalAlwaysPullsFull(t *testing.T) {
	up := newCounting(issue("1", "Todo"))
	tr, st, err := store.Wrap(up, storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	full := up.listCalls()
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, 2*full, up.listCalls())
}

// TestRunRetriesAFailedPullWithBackoff: after a failed pull Run tries again
// after RetryBase, not after a whole sync interval.
func TestRunRetriesAFailedPullWithBackoff(t *testing.T) {
	old := store.RetryBase
	store.RetryBase = 20 * time.Millisecond
	t.Cleanup(func() { store.RetryBase = old })
	up := newChanges(issue("1", "Todo"))
	up.fail.Store(true)
	_, st, err := store.Wrap(up, storeConfig(t, t.TempDir())) // SyncInterval: an hour
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); st.Run(ctx) }()
	defer func() { cancel(); <-done }()
	require.Eventually(t, func() bool { return up.candidates.Load() >= 3 }, 2*time.Second, 5*time.Millisecond,
		"failed pulls are retried within moments, not an hour")
	up.fail.Store(false)
	require.Eventually(t, func() bool { _, err := st.Status(); return err == nil && st.CursorForTest() != (time.Time{}) },
		2*time.Second, 5*time.Millisecond, "and the retry succeeds once the tracker answers")
}

// TestLocalWriteDuringIncrementalPullIsKept: a state the daemon wrote while
// an incremental pull was in flight is newer than the listing that pull
// read, and is not overwritten by it.
func TestLocalWriteDuringIncrementalPullIsKept(t *testing.T) {
	up := newChanges(issue("1", "Todo"))
	st, cands := incrementalStore(t, up, t.TempDir())
	up.setTitle("1", "Renamed") // listed by the next pull, still in Todo
	up.afterInc = func() {
		require.NoError(t, st.UpdateIssueState(context.Background(), "1", "Done"))
	}
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Empty(t, cands(), "the local move to Done stands")
	board, err := storeIssues(t, st)
	require.NoError(t, err)
	assert.Equal(t, "Done", board["1"].State)
}

// TestFullPullKeepsTheNewestCopyOfAnIssueListedTwice: an issue edited
// between two of a full pull's requests is listed in both states; the store
// keeps the newer copy, in the lists that copy's state belongs to, so the
// cursor (the newest update read) never moves past an update it does not
// hold. The two views are applied in map order, so this runs several times.
func TestFullPullKeepsTheNewestCopyOfAnIssueListedTwice(t *testing.T) {
	for range 64 {
		up := newChanges(issue("1", "Todo"))
		cfg := storeConfig(t, t.TempDir())
		cfg.Views = [][]string{board, {"Todo", "Done"}}
		up.afterList = func() { up.setState("1", "Done") } // after the first view is read
		tr, st, err := store.Wrap(up, cfg)
		require.NoError(t, err)
		cands, err := tr.FetchCandidateIssues(t.Context())
		require.NoError(t, err)
		assert.Empty(t, identifiers(cands), "Done is not a candidate state")
		for _, states := range cfg.Views {
			list, err := st.FetchIssuesByStates(t.Context(), states)
			require.NoError(t, err)
			require.Len(t, list, 1)
			assert.Equal(t, "Done", list[0].State, "the newer copy")
		}
		assert.Equal(t, up.clock, st.CursorForTest())
	}
}

// TestIncrementalPullListsALateCommittedUpdate: an update the tracker
// commits after a pull read past it, stamped a little before the cursor, is
// still listed by the next pull (store.CursorOverlap).
func TestIncrementalPullListsALateCommittedUpdate(t *testing.T) {
	up := newChanges(issue("1", "Todo"), issue("2", "Todo"))
	st, cands := incrementalStore(t, up, t.TempDir())
	up.setState("2", "In Progress") // moves the cursor
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	cursor := st.CursorForTest()

	up.setStateAt("1", "Done", cursor.Add(-30*time.Second))
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, []string{"ENG-2"}, cands(), "the late update is applied")
}

// TestRequestSyncMakesTheNextPullFull: a pull a webhook asks for
// (RequestSync) is a full one even when an incremental pull is due, since a
// changes listing does not show a deleted or transferred GitHub issue; the
// pull after it is incremental again.
func TestRequestSyncMakesTheNextPullFull(t *testing.T) {
	up := newChanges(issue("1", "Todo"))
	st, _ := incrementalStore(t, up, t.TempDir())
	full, inc := up.listCalls(), up.inc.Load()

	st.RequestSync()
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Greater(t, up.listCalls(), full, "a full pull")
	assert.Equal(t, inc, up.inc.Load(), "not an incremental one")

	full = up.listCalls()
	require.NoError(t, st.RequestSyncNowForTest(t.Context()))
	assert.Equal(t, full, up.listCalls(), "then incremental again")
	assert.Equal(t, inc+1, up.inc.Load())
}
