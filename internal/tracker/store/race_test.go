package store_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// barrier holds one tracker response after it was computed: the reply is
// what the tracker said at call time, delivered once the test releases it.
type barrier struct {
	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func (b *barrier) arm() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.armed, b.entered, b.release = true, make(chan struct{}), make(chan struct{})
}

// hold blocks the first call after arm until release.
func (b *barrier) hold() {
	b.mu.Lock()
	if !b.armed {
		b.mu.Unlock()
		return
	}
	b.armed = false
	entered, release := b.entered, b.release
	b.mu.Unlock()
	close(entered)
	<-release
}

func (b *barrier) wait(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the held call never started")
	}
}

// heldTracker is a project-scoped remote tracker whose candidate and
// by-state replies can be held: a candidate's project is its first label.
type heldTracker struct {
	*countingTracker
	candidates, byStates barrier
	mu                   sync.Mutex
	filter               []string
}

func (h *heldTracker) inScope(iss domain.Issue) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.filter == nil || (len(iss.Labels) > 0 && slices.Contains(h.filter, iss.Labels[0]))
}

func (h *heldTracker) scoped(list []domain.Issue) []domain.Issue {
	return slices.DeleteFunc(list, func(i domain.Issue) bool { return !h.inScope(i) })
}

func (h *heldTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	list, err := h.countingTracker.FetchCandidateIssues(ctx)
	list = h.scoped(list)
	h.candidates.hold()
	return list, err
}

func (h *heldTracker) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	list, err := h.countingTracker.FetchIssuesByStates(ctx, states)
	list = h.scoped(list)
	h.byStates.hold()
	return list, err
}

func (h *heldTracker) FetchProjects(context.Context) ([]domain.Project, error) { return nil, nil }

func (h *heldTracker) GetProjectFilter() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.filter
}

func (h *heldTracker) SetProjectFilter(slugs []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.filter = slugs
}

func projectIssue(id, state, project string) domain.Issue {
	i := issue(id, state)
	i.Labels = []string{project}
	return i
}

// readDuringHeldPull starts a cold read whose candidate pull is held, runs
// change while it is held, then releases it and returns what the read got.
func readDuringHeldPull(t *testing.T, h *heldTracker, tr tracker.Tracker, change func()) []domain.Issue {
	t.Helper()
	h.candidates.arm()
	got := make(chan []domain.Issue, 1)
	go func() {
		cands, err := tr.FetchCandidateIssues(context.Background())
		assert.NoError(t, err)
		got <- cands
	}()
	h.candidates.wait(t)
	change()
	close(h.candidates.release)
	return <-got
}

// TestStoreScopeChangeDuringPull: active states or a project filter changed
// while a pull is in flight win over that pull; its old-scope result is
// never committed, and the next read answers for the new scope.
func TestStoreScopeChangeDuringPull(t *testing.T) {
	t.Run("active states", func(t *testing.T) {
		h := &heldTracker{countingTracker: newCounting(issue("1", "Todo"), issue("2", "Review"))}
		tr, _, err := store.Wrap(h, storeConfig(t, t.TempDir()))
		require.NoError(t, err)
		got := readDuringHeldPull(t, h, tr, func() {
			tr.(tracker.StateListSetter).SetStateLists([]string{"Review"}, terminal)
		})
		assert.Equal(t, []string{"ENG-2"}, identifiers(got), "the read in flight answers for the new states")
		cands, err := tr.FetchCandidateIssues(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"ENG-2"}, identifiers(cands))
	})
	t.Run("project filter", func(t *testing.T) {
		h := &heldTracker{countingTracker: newCounting(projectIssue("1", "Todo", "old"), projectIssue("2", "Todo", "new"))}
		h.filter = []string{"old"}
		tr, _, err := store.Wrap(h, storeConfig(t, t.TempDir()))
		require.NoError(t, err)
		pm, ok := tr.(tracker.ProjectManager)
		require.True(t, ok)
		got := readDuringHeldPull(t, h, tr, func() { pm.SetProjectFilter([]string{"new"}) })
		assert.Equal(t, []string{"ENG-2"}, identifiers(got), "the read in flight answers for the new project")
		cands, err := tr.FetchCandidateIssues(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"ENG-2"}, identifiers(cands), "the old project's issues are not served")
	})
}

// TestStoreNewViewFollowsWritesDuringItsFetch: Itervox moving issues while a
// first-time state set is fetched decides their membership in that set, in
// both directions.
func TestStoreNewViewFollowsWritesDuringItsFetch(t *testing.T) {
	h := &heldTracker{countingTracker: newCounting(issue("1", "Todo"), issue("2", "Done"))}
	tr, _, err := store.Wrap(h, storeConfig(t, t.TempDir()))
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context()) // cold pull, not held
	require.NoError(t, err)

	todo := []string{"Todo"} // not a configured view: fetched on first read
	h.byStates.arm()
	got := make(chan []domain.Issue, 1)
	go func() {
		list, err := tr.FetchIssuesByStates(context.Background(), todo)
		assert.NoError(t, err)
		got <- list
	}()
	h.byStates.wait(t)
	require.NoError(t, tr.UpdateIssueState(t.Context(), "1", "Done"))
	require.NoError(t, tr.UpdateIssueState(t.Context(), "2", "Todo"))
	close(h.byStates.release)
	assert.Equal(t, []string{"ENG-2"}, identifiers(<-got), "the first read already reflects the moves")

	again, err := tr.FetchIssuesByStates(t.Context(), todo)
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-2"}, identifiers(again))
	for _, i := range again {
		assert.Equal(t, "Todo", i.State)
	}
	done, err := tr.FetchIssuesByStates(t.Context(), terminal)
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1"}, identifiers(done))
}
