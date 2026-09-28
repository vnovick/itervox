package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// M4-close D3 — AvailableSlots reporting 0 while draining is the ONLY gate
// for automation runs and pending-input resumes (neither goes through
// dispatch()). Each case runs a positive control (not draining: the work
// starts) so the drain case cannot pass vacuously; removing the Draining
// check from AvailableSlots makes the drain case start a worker.
func TestDrainGateHoldsAutomationsAndPendingInputResumes(t *testing.T) {
	newOrch := func(t *testing.T) (*Orchestrator, domain.Issue, context.Context) {
		cfg := automationBaseCfg()
		cfg.Agent.MaxConcurrentAgents = 3
		cfg.Agent.MaxTurns = 1
		issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
		mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
		rr := &gatedRunner{release: make(chan struct{})}
		o := New(cfg, mt, rr, nil)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(func() {
			cancel()
			close(rr.release)
			o.workersWg.Wait(5 * time.Second)
		})
		return o, issue, ctx
	}

	for _, draining := range []bool{false, true} {
		name := map[bool]string{false: "control", true: "draining"}[draining]

		t.Run("automation/"+name, func(t *testing.T) {
			o, issue, ctx := newOrch(t)
			state := NewState(o.cfg)
			state.MaxConcurrentAgents = 3
			state.Draining = draining
			started := o.startAutomationRun(ctx, &state, issue, time.Now(), AutomationDispatch{
				AutomationID: "a1", ProfileName: "responder", Trigger: AutomationTriggerContext{Type: "manual"},
			})
			if draining {
				assert.False(t, started, "no automation run starts while draining")
				assert.Empty(t, state.Running)
			} else {
				require.True(t, started, "control: the automation starts when not draining")
				assert.Len(t, state.Running, 1)
			}
		})

		t.Run("pending_input_resume/"+name, func(t *testing.T) {
			o, _, ctx := newOrch(t)
			state := NewState(o.cfg)
			state.MaxConcurrentAgents = 3
			state.Draining = draining
			state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
				IssueID: "id1", Identifier: "ENG-1", Context: "Need approval",
				UserMessage: "Approved.", QueuedAt: time.Now(),
			}
			state = o.processPendingInputResumes(ctx, state, time.Now())
			if draining {
				assert.Empty(t, state.Running, "no pending-input resume starts while draining")
				assert.Contains(t, state.PendingInputResumes, "ENG-1", "the reply stays queued for the next start")
			} else {
				require.Len(t, state.Running, 1, "control: the resume starts when not draining")
			}
		})
	}
}
