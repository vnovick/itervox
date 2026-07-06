package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

type blockedRunner struct{}

func (r *blockedRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	<-ctx.Done()
	return agent.TurnResult{Failed: true}, ctx.Err()
}

func testConfig() *config.Config {
	return &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:   []string{"In Progress"},
			TerminalStates: []string{"Done"},
		},
		Polling: config.PollingConfig{IntervalMs: 20},
		Agent: config.AgentConfig{
			Command:             "claude",
			MaxConcurrentAgents: 1,
			MaxTurns:            3,
			ReadTimeoutMs:       1000,
			TurnTimeoutMs:       1000,
		},
		PromptTemplate: "Handle {{ issue.identifier }}",
	}
}

func TestProvideInputSeedsAgentSessionIDNotRunLogSessionID(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "In Progress",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{
		IssueID:    "id1",
		Identifier: "ENG-1",
		SessionID:  "agent-session-1",
		Context:    "Need approval",
		Backend:    "codex",
		Command:    "codex",
		QueuedAt:   time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	state = orch.handleEvent(ctx, state, OrchestratorEvent{
		Type:       EventProvideInput,
		Identifier: "ENG-1",
		Message:    "Approved.",
	})
	cancel()

	entry, ok := state.Running["id1"]
	require.True(t, ok)
	require.NotNil(t, entry)
	assert.Empty(t, entry.SessionID, "run log session ID should stay empty until the worker publishes its own run log ID")
	assert.Equal(t, "agent-session-1", entry.AgentSessionID, "the recovered agent session belongs in AgentSessionID, not SessionID")
}

func TestPendingInputResumeSurvivesRetryableWorkerFailure(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "In Progress",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		SessionID:   "agent-session-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		Backend:     "codex",
		Command:     "codex",
		QueuedAt:    time.Now(),
	}
	state.Running["id1"] = &RunEntry{
		Issue:              issue,
		AgentSessionID:     "agent-session-1",
		PendingInputResume: true,
		StartedAt:          time.Now(),
	}
	state.Claimed["id1"] = struct{}{}

	state = orch.handleEvent(context.Background(), state, OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: "id1",
		RunEntry: &RunEntry{
			Issue:          issue,
			TerminalReason: TerminalFailed,
		},
		Error: assert.AnError,
	})

	entry, ok := state.PendingInputResumes["ENG-1"]
	require.True(t, ok, "retryable failure before progress must preserve the pending reply")
	require.NotNil(t, entry)
	assert.Equal(t, "Approved.", entry.UserMessage)
}

func TestPendingInputResumeClearsOnSuccess(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "Done",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		SessionID:   "agent-session-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		Backend:     "codex",
		Command:     "codex",
		QueuedAt:    time.Now(),
	}
	state.Running["id1"] = &RunEntry{
		Issue:              issue,
		AgentSessionID:     "agent-session-1",
		PendingInputResume: true,
		StartedAt:          time.Now(),
	}
	state.Claimed["id1"] = struct{}{}

	state = orch.handleEvent(context.Background(), state, OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: "id1",
		RunEntry: &RunEntry{
			Issue:          issue,
			TerminalReason: TerminalSucceeded,
		},
	})

	_, ok := state.PendingInputResumes["ENG-1"]
	assert.False(t, ok, "successful resumed run should clear the pending reply")
}

func TestPendingInputResumeClearsWhenIssueIsPaused(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "In Progress",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		QueuedAt:    time.Now(),
	}
	state.PausedIdentifiers["ENG-1"] = "id1"

	state = orch.processPendingInputResumes(context.Background(), state, time.Now())

	_, ok := state.PendingInputResumes["ENG-1"]
	assert.False(t, ok, "paused issues should drop stale pending replies")
}

func TestPendingInputResumeClearsWhenIssueLeavesActiveState(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Needs input",
		State:      "Backlog",
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := New(cfg, mt, &blockedRunner{}, nil)

	state := NewState(cfg)
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID:     "id1",
		Identifier:  "ENG-1",
		Context:     "Need approval",
		UserMessage: "Approved.",
		QueuedAt:    time.Now(),
	}

	state = orch.processPendingInputResumes(context.Background(), state, time.Now())

	_, ok := state.PendingInputResumes["ENG-1"]
	assert.False(t, ok, "non-active issues should drop stale pending replies")
}
