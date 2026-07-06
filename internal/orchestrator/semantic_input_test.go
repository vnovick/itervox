package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

type blockingQuestionRunner struct {
	mu      sync.Mutex
	calls   int
	prompts []string
}

func (r *blockingQuestionRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()

	return agent.TurnResult{
		SessionID:    "work-session-1",
		InputTokens:  12,
		OutputTokens: 7,
		AllTextBlocks: []string{
			"Implementation complete. What would you like to do?\n\n1. Merge back to main locally\n2. Push and create a Pull Request\n3. Keep the branch as-is (I'll handle it later)\n4. Discard this work\n\nWhich option?",
		},
	}, nil
}

func (r *blockingQuestionRunner) snapshot() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, append([]string{}, r.prompts...)
}

func TestPlainEnglishBlockingQuestionQueuesInputRequired(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 2
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Complete {{ issue.identifier }}"

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &blockingQuestionRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	deadline := time.After(3 * time.Second)
	for {
		snap := orch.Snapshot()
		entry, ok := snap.InputRequiredIssues["ENG-1"]
		if ok {
			calls, prompts := runner.snapshot()
			require.Equal(t, 1, calls, "plain-English detection must not run a second classifier turn")
			assert.Contains(t, entry.Context, "Which option?")
			assert.Len(t, prompts, 1)
			// The rendered WORKFLOW.md is the lead of the prompt; the worker
			// also appends a Run Context block carrying the handoff path and
			// run timestamp (v0.2.0: file-backed agent handoff). The test
			// pins only the WORKFLOW.md part.
			assert.True(t, strings.HasPrefix(prompts[0], "Complete ENG-1"),
				"WORKFLOW.md should lead the prompt; got %q", prompts[0])
			assert.Contains(t, prompts[0], "## Run Context",
				"Run Context block must be appended for handoff plumbing")
			history := orch.RunHistory()
			require.Len(t, history, 1)
			assert.Equal(t, "input_required", history[0].Status)
			assert.Equal(t, 1, history[0].TurnCount)
			assert.Equal(t, 12, history[0].InputTokens)
			assert.Equal(t, 7, history[0].OutputTokens)
			assert.Equal(t, 19, history[0].TotalTokens)

			issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
			require.NoError(t, err)
			require.Len(t, issues, 1)
			assert.Equal(t, "In Progress", issues[0].State, "blocking question must not transition to completion state")
			return
		}
		select {
		case <-deadline:
			issues, _ := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
			calls, prompts := runner.snapshot()
			t.Fatalf("issue did not enter input_required; state=%v calls=%d prompts=%q snap=%+v", issues, calls, prompts, orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
