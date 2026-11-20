package orchestrator

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// The dispatch claim check against the tracker scope (#116, #198).

// scopedHumanTracker is a humanTracker with a tracker scope
// (tracker.ScopeMatcher), evaluated in memory on the issue as read. Its
// reads by ID are unscoped, as the remote adapters' are.
type scopedHumanTracker struct {
	*humanTracker
	scopeMu sync.Mutex
	scope   func(domain.Issue) (match, known bool)
}

var _ tracker.ScopeMatcher = (*scopedHumanTracker)(nil)

func (s *scopedHumanTracker) MatchesFilter(issue domain.Issue) (bool, bool) {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	return s.scope(issue)
}

// setScope changes the scope: a person reassigned an issue, removed its
// label or moved it.
func (s *scopedHumanTracker) setScope(scope func(domain.Issue) (bool, bool)) {
	s.scopeMu.Lock()
	defer s.scopeMu.Unlock()
	s.scope = scope
}

// engProject is the scope of the ENG project: an issue moved to another
// project gets that project's key.
func engProject(issue domain.Issue) (bool, bool) {
	return strings.HasPrefix(issue.Identifier, "ENG-"), true
}

// withoutID is engProject without the issue with id (reassigned away).
func withoutID(id string) func(domain.Issue) (bool, bool) {
	return func(issue domain.Issue) (bool, bool) {
		if issue.ID == id {
			return false, true
		}
		return engProject(issue)
	}
}

// scopedClaimOrchestrator is an orchestrator whose tracker is the local
// store over a scopedHumanTracker with the engProject scope, pulled once:
// from here on its list reads are the local copy, and setScope makes that
// copy stale.
func scopedClaimOrchestrator(t *testing.T, issues ...domain.Issue) (*Orchestrator, *scopedHumanTracker, tracker.Tracker) {
	t.Helper()
	o, _ := breakerOrchestrator(t, fallbackOn())
	s := &scopedHumanTracker{humanTracker: newHumanTracker(issues...), scope: engProject}
	st, _, err := store.Wrap(s, store.Config{
		Path: filepath.Join(t.TempDir(), "tracker_store.json"), Scope: "test",
		ActiveStates: o.cfg.Tracker.ActiveStates, SyncInterval: time.Hour,
	})
	require.NoError(t, err)
	_, ok := st.(tracker.ScopeMatcher)
	require.True(t, ok, "the store forwards the scope check")
	o.tracker = st
	_, err = st.FetchCandidateIssues(context.Background()) // the last pull
	require.NoError(t, err)
	return o, s, st
}

func storeLists(t *testing.T, st tracker.Tracker, id string) bool {
	t.Helper()
	cands, err := st.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	return slices.ContainsFunc(cands, func(i domain.Issue) bool { return i.ID == id })
}

// TestDispatchClaimRefusesAnIssueOutsideTheTrackerScope: the dispatch claim
// read is unscoped, so admitClaimRead asks the tracker's ScopeMatcher about
// it, as fireRetries does (#198). An issue the read shows outside the scope
// (reassigned, or moved to another project) is not started, and the store
// forgets it. An issue in the scope is started, and so is one whose scope is
// not known yet: only a definite no refuses. A tracker without ScopeMatcher
// starts the issue as before.
func TestDispatchClaimRefusesAnIssueOutsideTheTrackerScope(t *testing.T) {
	local := breakerIssue("1") // the store's copy, ENG-1
	moved := local
	moved.Identifier = "OPS-7" // the tracker's copy after a move
	for _, tc := range []struct {
		name    string
		tracked domain.Issue // the issue as the tracker has it
		scope   func(domain.Issue) (bool, bool)
		starts  bool
	}{
		{"left the scope", local, withoutID(local.ID), false},
		{"moved to another project", moved, engProject, false},
		{"in the scope", local, engProject, true},
		{"scope not known yet", local, func(domain.Issue) (bool, bool) { return false, false }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o, s, st := scopedClaimOrchestrator(t, tc.tracked)
			s.setScope(tc.scope)
			detailed, err := o.tracker.FetchIssueDetail(ctx, local.ID)
			require.NoError(t, err)
			state := NewState(o.cfg)
			dispatched, _ := o.admitClaimRead(ctx, &state, local, detailed, nil, time.Now())
			assert.Equal(t, tc.starts, dispatched)
			assert.Equal(t, tc.starts, storeLists(t, st, local.ID), "the store forgets only a refused issue")
		})
	}
	t.Run("a tracker without ScopeMatcher", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		o, h, st := claimOrchestrator(t, moved)
		_, isMatcher := tracker.Tracker(h).(tracker.ScopeMatcher)
		require.False(t, isMatcher)
		detailed, err := o.tracker.FetchIssueDetail(ctx, local.ID)
		require.NoError(t, err)
		state := NewState(o.cfg)
		dispatched, _ := o.admitClaimRead(ctx, &state, local, detailed, nil, time.Now())
		assert.True(t, dispatched, "no scope to check: started as before")
		assert.True(t, storeLists(t, st, local.ID), "and not forgotten")
	})
}

// TestIssueThatLeftTheScopeLeavesTheStoreAtTheClaimCheck: an issue that left
// the tracker scope after the last pull (the store still lists it) is not
// started by the tick, and the store stops listing it at once, so the next
// tick does not pick it and read it again. The issue beside it, still in the
// scope, is dispatched.
func TestIssueThatLeftTheScopeLeavesTheStoreAtTheClaimCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gone, kept := breakerIssue("1"), breakerIssue("2")
	o, s, st := scopedClaimOrchestrator(t, gone, kept)
	s.setScope(withoutID(gone.ID))
	require.True(t, storeLists(t, st, gone.ID), "precondition: the store's copy is stale")

	// As in TestDeletedIssueLeavesTheStoreAtTheClaimCheck, no dependency
	// audit refresh reads the issue by ID on its own here.
	state := NewState(o.cfg)
	state.DepsRefreshInFlight, state.DepsRefreshStartedAt = true, time.Now()
	state = o.onTick(ctx, state)
	assert.NotContains(t, state.Running, gone.ID, "the issue that left the scope is not started")
	assert.Contains(t, state.Running, kept.ID, "the issue in the scope is dispatched")
	assert.False(t, storeLists(t, st, gone.ID), "the store no longer lists it")
	reads := s.detailReads(gone.ID)
	require.Equal(t, 1, reads, "the claim check read it once")
	_ = o.onTick(ctx, state)
	assert.Equal(t, reads, s.detailReads(gone.ID), "and the next tick does not read it again")
}

// TestScopeIsCheckedOnDispatchAndRetryOnly pins which admission paths check
// the tracker scope on their read by ID: the tick's dispatch and the retry
// (fireRetries) do not start an issue that left the scope. The reviewer, the
// automation, the backend-fallback re-dispatch and the input resume start it,
// as they did before the dispatch check: whether a run that continues work
// on an issue should stop when the issue leaves the scope is not decided
// here.
func TestScopeIsCheckedOnDispatchAndRetryOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		starts bool
		admit  func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State
	}{
		{admitDispatch, false, func(ctx context.Context, o *Orchestrator, state State, _ domain.Issue) State {
			return o.onTick(ctx, state)
		}},
		{admitRetry, false, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			state.Claimed[issue.ID] = struct{}{}
			state.RetryAttempts[issue.ID] = &RetryEntry{
				IssueID: issue.ID, Identifier: issue.Identifier, Attempt: 1, DueAt: time.Now().Add(-time.Second),
			}
			return o.fireRetries(ctx, state, time.Now())
		}},
		{admitReviewer, true, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
			return state
		}},
		{admitAutomation, true, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			_, _ = o.startAutomationRunOrHold(ctx, &state, issue, time.Now(), AutomationDispatch{ProfileName: "coder"})
			return state
		}},
		{admitFallback, true, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			state.IssueProfiles[issue.Identifier] = "coder"
			return limitExit(ctx, o, state, issue, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
		}},
		{admitInputResume, true, func(ctx context.Context, o *Orchestrator, state State, issue domain.Issue) State {
			state.PendingInputResumes[issue.Identifier] = &PendingInputResumeEntry{
				IssueID: issue.ID, Identifier: issue.Identifier, SessionID: "s1", UserMessage: "go on",
			}
			return o.processPendingInputResumes(ctx, state, time.Now())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			issue := breakerIssue("1")
			o, s, _ := scopedClaimOrchestrator(t, issue)
			s.setScope(withoutID(issue.ID))

			state := tc.admit(ctx, o, NewState(o.cfg), issue)
			_, running := state.Running[issue.ID]
			assert.Equal(t, tc.starts, running, "started on the %s path", tc.name)
		})
	}
}
