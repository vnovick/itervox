package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/linear"
	"github.com/vnovick/itervox/internal/tracker/store"
)

var (
	active   = []string{"Todo", "In Progress"}
	terminal = []string{"Done"}
	board    = []string{"Backlog", "Todo", "In Progress", "Done"}
)

// countingTracker is a remote-adapter stand-in that counts the list and
// state reads the store is meant to absorb.
type countingTracker struct {
	*tracker.MemoryTracker
	candidates atomic.Int64
	byStates   atomic.Int64
	byIDs      atomic.Int64
	fail       atomic.Bool
}

func newCounting(issues ...domain.Issue) *countingTracker {
	return &countingTracker{MemoryTracker: tracker.NewMemoryTracker(issues, active, terminal)}
}

var errDown = errors.New("tracker down")

func (c *countingTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	c.candidates.Add(1)
	if c.fail.Load() {
		return nil, errDown
	}
	return c.MemoryTracker.FetchCandidateIssues(ctx)
}

func (c *countingTracker) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	c.byStates.Add(1)
	if c.fail.Load() {
		return nil, errDown
	}
	return c.MemoryTracker.FetchIssuesByStates(ctx, states)
}

func (c *countingTracker) FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]domain.Issue, error) {
	c.byIDs.Add(1)
	return c.MemoryTracker.FetchIssueStatesByIDs(ctx, ids)
}

func (c *countingTracker) RateLimitSnapshot() *tracker.RateLimitSnapshot { return nil }

func (c *countingTracker) listCalls() int64 { return c.candidates.Load() + c.byStates.Load() }

func issue(id, state string) domain.Issue {
	return domain.Issue{ID: id, Identifier: "ENG-" + id, Title: "Issue " + id, State: state}
}

func storeConfig(t *testing.T, dir string) store.Config {
	t.Helper()
	return store.Config{
		Path:         filepath.Join(dir, "tracker_store.json"),
		Scope:        "linear||proj|in progress,todo",
		ActiveStates: active,
		Views:        [][]string{board},
		SyncInterval: time.Hour,
	}
}

func identifiers(issues []domain.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Identifier)
	}
	return out
}

// TestStoreColdStartPullsExactlyOnce: many readers racing a cold store cause
// one full pull (candidates + each configured view), and every read after it
// is answered without a list call.
func TestStoreColdStartPullsExactlyOnce(t *testing.T) {
	up := newCounting(issue("1", "Todo"), issue("2", "Backlog"), issue("3", "Done"))
	tr, _, err := store.Wrap(up, storeConfig(t, t.TempDir()))
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := tr.FetchCandidateIssues(t.Context())
			assert.NoError(t, err)
			_, err = tr.FetchIssuesByStates(t.Context(), board)
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.Equal(t, int64(1), up.candidates.Load(), "one candidate pull")
	assert.Equal(t, int64(1), up.byStates.Load(), "one pull per configured view")

	cands, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1"}, identifiers(cands))
	all, err := tr.FetchIssuesByStates(t.Context(), []string{"done", "BACKLOG", "todo", "In Progress"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ENG-1", "ENG-2", "ENG-3"}, identifiers(all), "state sets match regardless of order and case")
	assert.Equal(t, int64(2), up.listCalls(), "no list call after the cold pull")
}

// TestStoreSurvivesRestart: a second store over the same file serves the
// first one's pull without calling the tracker; a file for another scope is
// a cold start.
func TestStoreSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	up := newCounting(issue("1", "Todo"), issue("2", "Done"))
	first, _, err := store.Wrap(up, storeConfig(t, dir))
	require.NoError(t, err)
	_, err = first.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(dir, "tracker_store.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the file holds issue text")

	up2 := newCounting() // a restarted daemon's adapter
	second, _, err := store.Wrap(up2, storeConfig(t, dir))
	require.NoError(t, err)
	cands, err := second.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1"}, identifiers(cands))
	all, err := second.FetchIssuesByStates(t.Context(), board)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ENG-1", "ENG-2"}, identifiers(all))
	assert.Zero(t, up2.listCalls(), "a restart serves from the file")

	other := storeConfig(t, dir)
	other.Scope = "linear||another-project|in progress,todo"
	up3 := newCounting(issue("9", "Todo"))
	third, _, err := store.Wrap(up3, other)
	require.NoError(t, err)
	cands, err = third.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-9"}, identifiers(cands), "another project's file is not served")
	assert.Equal(t, int64(1), up3.candidates.Load())
}

// TestStoreWritesApplyAtOnce: a state move leaves or joins the candidate list
// and the views straight away, and a branch write is visible to the next
// state read, all without a list call.
func TestStoreWritesApplyAtOnce(t *testing.T) {
	up := newCounting(issue("1", "Todo"), issue("2", "Backlog"))
	tr, _, err := store.Wrap(up, storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	ctx := t.Context()
	_, err = tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	calls := up.listCalls()

	require.NoError(t, tr.UpdateIssueState(ctx, "1", "Done"))
	require.NoError(t, tr.UpdateIssueState(ctx, "2", "Todo"))
	require.NoError(t, tr.SetIssueBranch(ctx, "2", "eng-2-fix"))
	created, err := tr.CreateIssue(ctx, "2", "Follow-up", "body", "Todo")
	require.NoError(t, err)

	cands, err := tr.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ENG-2", created.Identifier}, identifiers(cands))
	all, err := tr.FetchIssuesByStates(ctx, board)
	require.NoError(t, err)
	byID := map[string]domain.Issue{}
	for _, i := range all {
		byID[i.ID] = i
	}
	assert.Equal(t, "Done", byID["1"].State)
	require.NotNil(t, byID["2"].BranchName)
	assert.Equal(t, "eng-2-fix", *byID["2"].BranchName)
	assert.Equal(t, calls, up.listCalls())
}

// TestStoreRestartKeepsViewsAndWrites: a restart after the store has run for
// longer than the unread-view lifetime still serves from the file, and a
// state Itervox wrote just before the restart is not undone by it.
func TestStoreRestartKeepsViewsAndWrites(t *testing.T) {
	dir := t.TempDir()
	cfg := storeConfig(t, dir)
	cfg.Views = [][]string{board, terminal} // terminal is read only at startup
	cfg.SyncInterval = 2 * time.Millisecond
	up := newCounting(issue("1", "Todo"), issue("2", "Done"))
	tr, s, err := store.Wrap(up, cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return up.candidates.Load() > 20 }, 5*time.Second, time.Millisecond,
		"well past 10 sync intervals")
	cancel()
	<-done
	require.NoError(t, tr.UpdateIssueState(t.Context(), "1", "Done"))

	cfg.SyncInterval = time.Hour
	up2 := newCounting()
	again, _, err := store.Wrap(up2, cfg)
	require.NoError(t, err)
	cands, err := again.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Empty(t, cands, "the completed issue is not a candidate again after a restart")
	done2, err := again.FetchIssuesByStates(t.Context(), terminal)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"ENG-1", "ENG-2"}, identifiers(done2))
	assert.Zero(t, up2.listCalls(), "a restart serves every configured view from the file")
}

// TestStoreStateReadsGoToTheTracker: reads by ID see a person's change at
// once (a worker deciding whether to continue must not read a stale state),
// and the change reaches the candidate list.
func TestStoreStateReadsGoToTheTracker(t *testing.T) {
	up := newCounting(issue("1", "Todo"))
	tr, _, err := store.Wrap(up, storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)

	up.SetIssueState("1", "Done") // moved by a person in the tracker
	states, err := tr.FetchIssueStatesByIDs(t.Context(), []string{"1"})
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, "Done", states[0].State)
	cands, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Empty(t, cands)
}

// TestStoreRunAndReaderShareTheColdPull: Run starting on a cold store while
// a reader also needs data makes one full pull between them.
func TestStoreRunAndReaderShareTheColdPull(t *testing.T) {
	for range 50 {
		up := newCounting(issue("1", "Todo"))
		tr, s, err := store.Wrap(up, storeConfig(t, t.TempDir()))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { s.Run(ctx); close(done) }()
		_, err = tr.FetchCandidateIssues(t.Context())
		require.NoError(t, err)
		require.Eventually(t, func() bool { at, _ := s.Status(); return !at.IsZero() }, time.Second, time.Millisecond)
		time.Sleep(time.Millisecond) // let Run's first pull, if any, run
		cancel()
		<-done
		require.Equal(t, int64(1), up.candidates.Load(), "one full pull on a cold start")
	}
}

// TestStoreServesThroughAnOutage: a failed sync keeps the last pull; reads go
// on answering from it.
func TestStoreServesThroughAnOutage(t *testing.T) {
	up := newCounting(issue("1", "Todo"))
	cfg := storeConfig(t, t.TempDir())
	cfg.SyncInterval = 20 * time.Millisecond
	tr, s, err := store.Wrap(up, cfg)
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	syncedAt, _ := s.Status()

	up.fail.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	require.Eventually(t, func() bool {
		_, e := s.Status()
		return e != nil
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	<-done

	cands, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1"}, identifiers(cands))
	at, lastErr := s.Status()
	assert.Equal(t, syncedAt, at)
	assert.ErrorIs(t, lastErr, errDown)
}

// TestStoreUnknownStateSetFetchedOnce: a state set outside the configured
// views costs one request, then is a view like the others.
func TestStoreUnknownStateSetFetchedOnce(t *testing.T) {
	up := newCounting(issue("1", "Todo"), issue("2", "Done"))
	tr, _, err := store.Wrap(up, storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	for range 3 {
		got, err := tr.FetchIssuesByStates(t.Context(), terminal)
		require.NoError(t, err)
		assert.Equal(t, []string{"ENG-2"}, identifiers(got))
	}
	assert.Equal(t, int64(2), up.byStates.Load(), "the cold pull's view, then one fetch for the new set")
}

// TestStoreStateListChangePullsAgain: new active states change which issues
// are candidates, so the next read pulls again.
func TestStoreStateListChangePullsAgain(t *testing.T) {
	up := newCounting(issue("1", "Todo"), issue("2", "Review"))
	tr, _, err := store.Wrap(up, storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)

	tr.(tracker.StateListSetter).SetStateLists([]string{"Review"}, terminal)
	cands, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-2"}, identifiers(cands))
	assert.Equal(t, int64(2), up.candidates.Load())
}

// TestStoreKeepsTheAdaptersOptionalInterfaces: an assertion on the store
// answers as it would on the adapter.
func TestStoreKeepsTheAdaptersOptionalInterfaces(t *testing.T) {
	tr, _, err := store.Wrap(newCounting(), storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	_, ok := tr.(tracker.IdempotentCommenter)
	assert.True(t, ok)
	_, ok = tr.(tracker.RateLimiter)
	assert.True(t, ok)
	_, ok = tr.(tracker.ProjectManager)
	assert.False(t, ok, "the counting tracker has no project filter")
	_, ok = tr.(tracker.DetailBatcher)
	assert.False(t, ok, "no project filter means the plain store, which does not batch")

	lin, _, err := store.Wrap(linear.NewClient(linear.ClientConfig{APIKey: "lin_test"}), storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	_, ok = lin.(tracker.ProjectManager)
	assert.True(t, ok, "Linear's project filter survives the store")
	_, ok = lin.(tracker.DetailBatcher)
	assert.True(t, ok, "Linear's batched detail survives the store")

	_, _, err = store.Wrap(tracker.NewMemoryTracker(nil, active, terminal), storeConfig(t, t.TempDir()))
	assert.Error(t, err, "the memory tracker is not a remote adapter")
}

// closedAsDone answers a "closed" query the way the GitHub adapter does:
// closed issues, with their state named after a terminal label.
type closedAsDone struct{ *countingTracker }

func (c closedAsDone) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	if len(states) == 1 && states[0] == "closed" {
		c.byStates.Add(1)
		return c.MemoryTracker.FetchIssuesByStates(ctx, []string{"Done"})
	}
	return c.countingTracker.FetchIssuesByStates(ctx, states)
}

// TestStoreKeepsTrackerNamedMemberships: an issue the tracker lists for a
// state set stays in it when its state is named differently (GitHub closed
// issues), through repeat reads and a detail refresh.
func TestStoreKeepsTrackerNamedMemberships(t *testing.T) {
	up := closedAsDone{newCounting(issue("1", "Todo"), issue("9", "Done"))}
	cfg := storeConfig(t, t.TempDir())
	cfg.Views = [][]string{active}
	tr, _, err := store.Wrap(up, cfg)
	require.NoError(t, err)
	for range 2 {
		got, err := tr.FetchIssuesByStates(t.Context(), []string{"closed"})
		require.NoError(t, err)
		assert.Equal(t, []string{"ENG-9"}, identifiers(got))
	}
	_, err = tr.FetchIssueDetail(t.Context(), "9")
	require.NoError(t, err)
	got, err := tr.FetchIssuesByStates(t.Context(), []string{"closed"})
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-9"}, identifiers(got), "a refresh with the same state keeps the membership")
}

// TestStoreDoesNotSaveBeforeTheFirstPull: a write while the cold pull is
// failing is not saved as a complete store, so a restart pulls instead of
// serving an empty board.
func TestStoreDoesNotSaveBeforeTheFirstPull(t *testing.T) {
	dir := t.TempDir()
	up := newCounting(issue("1", "Todo"))
	up.fail.Store(true)
	tr, _, err := store.Wrap(up, storeConfig(t, dir))
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context())
	require.ErrorIs(t, err, errDown)
	_, err = tr.CreateIssue(t.Context(), "1", "Follow-up", "", "Todo")
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(dir, "tracker_store.json"))

	up2 := newCounting(issue("1", "Todo"))
	again, _, err := store.Wrap(up2, storeConfig(t, dir))
	require.NoError(t, err)
	cands, err := again.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1"}, identifiers(cands))
	assert.Equal(t, int64(1), up2.candidates.Load(), "the restart pulls")
}
