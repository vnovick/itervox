package local_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/github"
	"github.com/vnovick/itervox/internal/tracker/linear"
	"github.com/vnovick/itervox/internal/tracker/local"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// ifaceSet lists which optional interfaces v implements, for comparing a
// join with the tracker it wraps.
func ifaceSet(v any) map[string]bool {
	_, rl := v.(tracker.RateLimiter)
	_, db := v.(tracker.DetailBatcher)
	_, ic := v.(tracker.IdempotentCommenter)
	_, pm := v.(tracker.ProjectManager)
	_, sls := v.(tracker.StateListSetter)
	_, sm := v.(tracker.ScopeManager)
	_, sma := v.(tracker.ScopeMatcher)
	_, fm := v.(tracker.FilterManager)
	_, up := v.(interface{ Upstream() tracker.Tracker })
	_, sps := v.(interface {
		SetPendingStates(func() map[string]string)
	})
	_, fg := v.(interface{ Forget(string) })
	_, tc := v.(interface {
		TrackerCopy(string) (domain.Issue, bool)
	})
	return map[string]bool{"RateLimiter": rl, "DetailBatcher": db, "IdempotentCommenter": ic, "ProjectManager": pm,
		"StateListSetter": sls, "ScopeManager": sm, "ScopeMatcher": sma, "FilterManager": fm,
		"Upstream": up, "SetPendingStates": sps, "Forget": fg, "TrackerCopy": tc}
}

// TestWithTasksKeepsTheTrackersInterfaces (#251): joined with local tasks,
// each real tracker shape (Linear, GitHub, the store over either) answers
// every type assertion its callers make exactly as it did before.
func TestWithTasksKeepsTheTrackersInterfaces(t *testing.T) {
	lc := linear.NewClient(linear.ClientConfig{APIKey: "x", ProjectSlug: "p"})
	gc := github.NewClient(github.ClientConfig{APIKey: "x", ProjectSlug: "o/r"})
	ls, _, err := store.Wrap(lc, store.Config{Path: filepath.Join(t.TempDir(), "l.json"), SyncInterval: time.Minute})
	require.NoError(t, err)
	gs, _, err := store.Wrap(gc, store.Config{Path: filepath.Join(t.TempDir(), "g.json"), SyncInterval: time.Minute})
	require.NoError(t, err)
	tasks := local.New(local.Config{Dir: t.TempDir(), Prefix: local.TaskPrefix})
	for name, tr := range map[string]tracker.Tracker{"linear": lc, "github": gc, "linear+store": ls, "github+store": gs} {
		joined := local.WithTasks(tr, tasks)
		assert.Equal(t, ifaceSet(tr), ifaceSet(joined), name)
		_, ok := joined.(local.TaskCreator)
		assert.True(t, ok, name)
	}
	// The memory tracker has none of them; the join adds only its own.
	mt := tracker.NewMemoryTracker(nil, []string{"Todo"}, []string{"Done"})
	got := ifaceSet(local.WithTasks(mt, tasks))
	for k, v := range got {
		assert.Equal(t, k == "StateListSetter", v, k)
	}
}

// TestWithTasksRoutesByID (#251): list reads return the tracker's issues and
// the tasks; reads and writes by ID go to the side that owns the ID; a
// follow-up of a task is a task; the tracker's filter does not apply to
// tasks.
func TestWithTasksRoutesByID(t *testing.T) {
	ctx := context.Background()
	active, terminal := []string{"Todo", "In Progress"}, []string{"Done"}
	mt := tracker.NewMemoryTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", Title: "Tracker", State: "Todo"}}, active, terminal)
	tasks := local.New(local.Config{Dir: t.TempDir(), Prefix: local.TaskPrefix, ActiveStates: active, TerminalStates: terminal})
	joined := local.WithTasks(mt, tasks)
	tc := joined.(local.TaskCreator)

	created, err := tc.CreateLocalTask(ctx, "A task", "", "Todo")
	require.NoError(t, err)
	assert.Equal(t, "TASK-1", created.Identifier)
	assert.True(t, tc.OwnsLocalTask("TASK-1"))
	assert.False(t, tc.OwnsLocalTask("ENG-1"), "another prefix is the tracker's")
	assert.False(t, tc.OwnsLocalTask("6f1c2a52-0b5e-4b8e-9a59-0d2e3f4a5b6c"), "a Linear ID")
	assert.False(t, tc.OwnsLocalTask("42"), "a GitHub ID")

	cands, err := joined.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	require.Len(t, cands, 2)
	assert.Equal(t, "ENG-1", cands[0].Identifier)
	assert.Equal(t, "TASK-1", cands[1].Identifier)

	byID, err := joined.FetchIssueStatesByIDs(ctx, []string{"TASK-1", "id1"})
	require.NoError(t, err)
	assert.Len(t, byID, 2)

	require.NoError(t, joined.UpdateIssueState(ctx, "TASK-1", "In Progress"))
	require.NoError(t, joined.SetIssueBranch(ctx, "TASK-1", "itervox/task-1"))
	_, err = joined.CreateComment(ctx, "TASK-1", "hello")
	require.NoError(t, err)
	task, err := joined.FetchIssueByIdentifier(ctx, "TASK-1")
	require.NoError(t, err)
	assert.Equal(t, "In Progress", task.State)
	require.Len(t, task.Comments, 1)
	eng, err := joined.FetchIssueDetail(ctx, "id1")
	require.NoError(t, err)
	assert.Equal(t, "Todo", eng.State, "the tracker's issue is untouched")

	follow, err := joined.CreateIssue(ctx, "TASK-1", tracker.NewIssue{Title: "Follow-up", State: "Todo"})
	require.NoError(t, err)
	assert.Equal(t, "TASK-2", follow.Identifier, "a task's follow-up is a task")
	other, err := joined.CreateIssue(ctx, "id1", tracker.NewIssue{Title: "Tracker follow-up", State: "Todo"})
	require.NoError(t, err)
	assert.False(t, tc.OwnsLocalTask(other.ID))

	byStates, err := joined.FetchIssuesByStates(ctx, []string{"In Progress"})
	require.NoError(t, err)
	require.Len(t, byStates, 1)
	assert.Equal(t, "TASK-1", byStates[0].Identifier)

	// A task is always in the tracker's scope.
	gc := github.NewClient(github.ClientConfig{APIKey: "x", ProjectSlug: "o/r"})
	sm := local.WithTasks(gc, tasks).(tracker.ScopeMatcher)
	match, known := sm.MatchesFilter(domain.Issue{ID: "TASK-1", Identifier: "TASK-1"})
	assert.True(t, match)
	assert.True(t, known)
}
