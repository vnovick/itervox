package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// statesRecordingTracker records every FetchIssuesByStates state set, so a
// test can see what the off-loop scan asked the tracker for.
type statesRecordingTracker struct {
	tracker.Tracker
	mu    sync.Mutex
	asked [][]string
}

func (r *statesRecordingTracker) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	r.mu.Lock()
	r.asked = append(r.asked, slices.Clone(states))
	r.mu.Unlock()
	return r.Tracker.FetchIssuesByStates(ctx, states)
}

func (r *statesRecordingTracker) askedStates() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
}

// applyRefreshIfLaunched folds in the batch a tick launched, if it launched
// one (before the fix the Backlog scan never launched).
func applyRefreshIfLaunched(t *testing.T, o *Orchestrator, state *State) {
	t.Helper()
	if !state.DepsRefreshInFlight {
		return
	}
	applyOffLoopDependencyRefresh(t, o, state)
}

// TestBlockersResolvedFiresForBacklogIssue: the documented "backlog → Todo"
// flow through the real audit path, on the local tracker the bug was found
// with. ITX-2 sits in Backlog (never a candidate) blocked by ITX-1. The
// off-loop scan must audit ITX-2 as blocked, and once ITX-1 reaches a
// terminal state the next scan (one refresh interval later) must observe the
// transition and fire the blockers_resolved rule — no audit row is seeded by
// hand. The scan reads only the states the rule can fire in.
func TestBlockersResolvedFiresForBacklogIssue(t *testing.T) {
	for _, tc := range []struct {
		name       string
		states     []string
		wantAsked  []string
		backlogCfg []string
	}{
		{name: "filter.states names Backlog", states: []string{"Backlog"}, wantAsked: []string{"Backlog"}, backlogCfg: []string{"Backlog"}},
		{name: "no filter.states scans tracker.backlog_states", states: nil, wantAsked: []string{"Backlog"}, backlogCfg: []string{"Backlog"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dependencyAuditConfig()
			enableDependencyRefreshForTest(cfg)
			cfg.Agent.DependencyAuditRefreshIntervalMs = 1
			cfg.Tracker.Kind = "local"
			cfg.Tracker.BacklogStates = tc.backlogCfg
			cfg.Agent.MaxConcurrentAgents = 1
			cfg.Agent.Profiles = map[string]config.AgentProfile{"pm": {AllowedActions: []string{config.AgentActionMoveState}}}

			dir := filepath.Join(t.TempDir(), "issues")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			write := func(name, content string) {
				p := filepath.Join(dir, name)
				var next time.Time
				if info, err := os.Stat(p); err == nil {
					next = info.ModTime().Add(2 * time.Second) // seen by the stat-based rescan
				}
				require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
				if !next.IsZero() {
					require.NoError(t, os.Chtimes(p, next, next))
				}
			}
			write("ITX-1.md", "---\ntitle: Blocker\nstate: Todo\n---\n")
			write("ITX-2.md", "---\ntitle: Dependent\nstate: Backlog\nblocked_by: [ITX-1]\n---\n")
			write("ITX-9.md", "---\ntitle: Busy\nstate: In Progress\n---\n")
			lt := local.New(local.Config{Dir: dir, ActiveStates: cfg.Tracker.ActiveStates, TerminalStates: cfg.Tracker.TerminalStates})
			rec := &statesRecordingTracker{Tracker: lt}

			o := New(cfg, rec, nil, nil)
			o.SetBlockersResolvedAutomations([]BlockersResolvedAutomation{{
				ID:          "unblock-backlog-to-todo",
				ProfileName: "pm",
				States:      tc.states,
				MoveToState: "Todo",
			}})
			state := NewState(cfg)
			// The one slot is taken, so the automation is queued rather than
			// started (and ITX-1, a candidate, is not dispatched either).
			busy, err := lt.FetchIssueDetail(t.Context(), "ITX-9")
			require.NoError(t, err)
			state.Running[busy.ID] = &RunEntry{Issue: *busy}

			// Tick 1: ITX-2 is not a candidate; only the scan can audit it.
			state = o.onTick(t.Context(), state)
			applyRefreshIfLaunched(t, o, &state)
			row := state.DependencyAudit["ITX-2"]
			require.NotNil(t, row, "the scan must audit the Backlog issue")
			require.Equal(t, DependencyAuditBlocked, row.Status)
			require.Empty(t, state.AutomationQueue)

			// The blocker lands.
			write("ITX-1.md", "---\ntitle: Blocker\nstate: Done\n---\n")
			time.Sleep(5 * time.Millisecond) // past the 1 ms refresh interval

			// Tick 2: the next scan sees ITX-2 unblocked and fires the rule.
			state = o.onTick(t.Context(), state)
			applyRefreshIfLaunched(t, o, &state)

			require.Len(t, state.AutomationQueue, 1, "blockers_resolved must fire for the Backlog issue")
			var queued *AutomationQueueEntry
			for _, entry := range state.AutomationQueue {
				queued = entry
			}
			assert.Equal(t, "ITX-2", queued.Issue.Identifier)
			assert.Equal(t, config.AutomationTriggerBlockersResolved, queued.Trigger.Type)
			assert.Equal(t, "Todo", queued.MoveToState)
			require.Len(t, queued.Trigger.ResolvedBlockers, 1)
			assert.Equal(t, "ITX-1", *queued.Trigger.ResolvedBlockers[0].Identifier)

			asked := rec.askedStates()
			require.NotEmpty(t, asked)
			for _, states := range asked {
				assert.ElementsMatch(t, tc.wantAsked, states, "the scan reads only the states the rule can fire in")
			}
		})
	}
}
