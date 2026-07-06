package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// ORCH-1: a worker exit with a non-Canceled error arriving AFTER a reconcile
// kill path removed the issue from Running must not panic the event loop.
func TestWorkerExitAfterReconcileKillDoesNotPanic(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Agent.MaxConcurrentAgents = 2
	cfg.Agent.MaxRetries = 3
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	// Simulate ReconcileStalls having already deleted the entry:
	// state.Running deliberately does NOT contain "id1".
	ev := OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: "id1",
		Error:   errors.New("turn 3: agent: read timeout after 30000ms idle"),
		RunEntry: &RunEntry{
			Issue:          domain.Issue{ID: "id1", Identifier: "ENG-1", State: "Todo"},
			TerminalReason: TerminalFailed,
			RetryAttempt:   intPtr(0),
		},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleEvent panicked with nil liveEntry: %v", r)
		}
	}()
	o.handleEvent(context.Background(), state, ev)
}

// ORCH-2: a late context.Canceled exit (stall-kill race) must not release the
// claim that ScheduleRetry re-established, and fireRetries must never
// dispatch over a live Running entry.
func TestLateCanceledExitKeepsRetryClaim(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Agent.MaxConcurrentAgents = 2
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	// Reconcile already: deleted Running, re-claimed via ScheduleRetry.
	state = ScheduleRetry(state, "id1", 1, "ENG-1", "stall timeout", time.Now(), 10)
	if _, ok := state.Claimed["id1"]; !ok {
		t.Fatal("precondition: ScheduleRetry must set Claimed")
	}
	ev := OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: "id1",
		Error:   context.Canceled,
		RunEntry: &RunEntry{
			Issue:          domain.Issue{ID: "id1", Identifier: "ENG-1", State: "Todo"},
			TerminalReason: TerminalFailed,
		},
	}
	state = o.handleEvent(context.Background(), state, ev)
	if _, ok := state.RetryAttempts["id1"]; ok {
		if _, claimed := state.Claimed["id1"]; !claimed {
			t.Fatal("late Canceled exit released the retry's claim — RetryAttempts⇒Claimed invariant broken (ORCH-2)")
		}
	}
}

// WORK-2: a failed named-profile run's handoff must be renamed .partial.md.
// sendExit's RunEntry carries no ProfileName/StartedAt (see worker.go
// sendExit) — the TerminalFailed handler must take those fields from the
// live Running entry (populated at dispatch, event_loop.go ~748-749)
// instead of the bare exit RunEntry.
func TestFailedExitMarksNamedProfileHandoffPartial(t *testing.T) {
	ws := t.TempDir()
	hd := filepath.Join(ws, ".itervox", "handoff")
	require.NoError(t, os.MkdirAll(hd, 0o755))
	started := time.Now().Add(-time.Minute)
	handoff := filepath.Join(hd, "2026-07-06T10-00-00.000Z_implementer.md")
	require.NoError(t, os.WriteFile(handoff, []byte("in-flight work"), 0o644))

	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Agent.MaxConcurrentAgents = 2
	cfg.Agent.MaxRetries = 3
	o := New(cfg, nil, nil, &stubWorkspaceProvider{path: ws})
	state := NewState(cfg)
	state.Running["id1"] = &RunEntry{
		Issue:       domain.Issue{ID: "id1", Identifier: "ENG-1", State: "Todo"},
		ProfileName: "implementer",
		StartedAt:   started,
	}
	ev := OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: "id1",
		Error:   errors.New("deliberate failure"),
		RunEntry: &RunEntry{
			Issue:          domain.Issue{ID: "id1", Identifier: "ENG-1"},
			TerminalReason: TerminalFailed,
			RetryAttempt:   intPtr(0),
		},
	}
	o.handleEvent(context.Background(), state, ev)

	_, err := os.Stat(strings.TrimSuffix(handoff, ".md") + ".partial.md")
	require.NoError(t, err, "failed run's handoff must be renamed .partial.md (WORK-2)")
}

// WORK-2 (sibling): a failed default-profile run must NOT rename a
// predecessor's complete handoff to .partial.md. Before the fix, sendExit's
// zero StartedAt disabled markLatestHandoffPartial's notBefore gate, so any
// older `_agent.md` file — including a predecessor's successfully completed
// deliverable — was eligible for renaming. The live entry's non-zero
// StartedAt must be used as the gate instead.
func TestFailedExitDoesNotRenamePredecessorHandoff(t *testing.T) {
	ws := t.TempDir()
	hd := filepath.Join(ws, ".itervox", "handoff")
	require.NoError(t, os.MkdirAll(hd, 0o755))
	predecessor := filepath.Join(hd, "2026-07-06T08-00-00.000Z_agent.md")
	require.NoError(t, os.WriteFile(predecessor, []byte("predecessor complete work"), 0o644))
	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(predecessor, old, old))

	started := time.Now().Add(-time.Minute)
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Agent.MaxConcurrentAgents = 2
	cfg.Agent.MaxRetries = 3
	o := New(cfg, nil, nil, &stubWorkspaceProvider{path: ws})
	state := NewState(cfg)
	state.Running["id1"] = &RunEntry{
		Issue:     domain.Issue{ID: "id1", Identifier: "ENG-1", State: "Todo"},
		StartedAt: started, // default profile: ProfileName == ""
	}
	ev := OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: "id1",
		Error:   errors.New("deliberate failure"),
		RunEntry: &RunEntry{
			Issue:          domain.Issue{ID: "id1", Identifier: "ENG-1"},
			TerminalReason: TerminalFailed,
			RetryAttempt:   intPtr(0),
		},
	}
	o.handleEvent(context.Background(), state, ev)

	_, err := os.Stat(predecessor)
	require.NoError(t, err, "predecessor's complete handoff must NOT be renamed (WORK-2)")
	_, err = os.Stat(strings.TrimSuffix(predecessor, ".md") + ".partial.md")
	require.True(t, os.IsNotExist(err), "predecessor must not gain a .partial.md sibling")
}
