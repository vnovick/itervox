package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// promptCapturingRunner records the prompt of every turn and ends the run.
type promptCapturingRunner struct {
	mu      sync.Mutex
	prompts []string
}

func (r *promptCapturingRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	return agent.TurnResult{ResultText: "done", InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
}

const switchResetAt = "2026-09-27T15:00:00Z"

// renderRecoveryPrompt runs the real runWorker once under profile
// "codex-coder" on backend codex, with a prior handoff in the workspace,
// and returns the first turn's prompt.
func renderRecoveryPrompt(t *testing.T, instructions string, notice *BackendSwitchNotice) string {
	t.Helper()
	ws := t.TempDir()
	hd := filepath.Join(ws, ".itervox", "handoff")
	require.NoError(t, os.MkdirAll(hd, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hd, "2026-09-27T10-00-00.000Z_claude-coder.md"), []byte("claude got halfway"), 0o644))

	cfg := automationBaseCfg()
	cfg.Agent.MaxTurns = 1
	cfg.Agent.Profiles["codex-coder"] = config.AgentProfile{Instructions: instructions}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCapturingRunner{}
	o := New(cfg, mt, runner, &stubWorkspaceProvider{path: ws})
	o.events = make(chan OrchestratorEvent, 64)
	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, "", "codex", "codex", "codex-coder", true, nil, nil, notice)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	require.NotEmpty(t, runner.prompts)
	return runner.prompts[0]
}

func testSwitchNotice() *BackendSwitchNotice {
	reset, _ := time.Parse(time.RFC3339, switchResetAt)
	return &BackendSwitchNotice{
		PreviousBackend: "claude", PreviousProfile: "claude-coder",
		Backend: "codex", Profile: "codex-coder",
		Reason: `rate_limited automation "switch-to-codex"`, LimitResetsAt: reset,
	}
}

func assertNoticeOnce(t *testing.T, p string) {
	t.Helper()
	require.Equal(t, 1, strings.Count(p, backendSwitchNoticeHeading), "the notice appears exactly once")
	notice := p[strings.Index(p, backendSwitchNoticeHeading):]
	assert.Contains(t, notice, "`claude`")
	assert.Contains(t, notice, "`claude-coder`")
	assert.Contains(t, notice, switchResetAt)
	envelope := strings.Index(p, "## Operator Reply Channel")
	handoffs := strings.Index(p, "## Prior Agent Handoffs")
	at := strings.Index(p, backendSwitchNoticeHeading)
	require.GreaterOrEqual(t, envelope, 0, "operatorReplyEnvelope present")
	require.GreaterOrEqual(t, handoffs, 0, "prior handoffs present")
	assert.Less(t, envelope, at)
	assert.Less(t, handoffs, at)
}

// TestWorkerPrompt_BackendSwitchNotice_RecoveryNoInstructions (CORE-101).
func TestWorkerPrompt_BackendSwitchNotice_RecoveryNoInstructions(t *testing.T) {
	assertNoticeOnce(t, renderRecoveryPrompt(t, "", testSwitchNotice()))
}

// TestWorkerPrompt_BackendSwitchNotice_RecoveryWithProfileInstructions
// (CORE-101): a profile's own INSTRUCTIONS cannot drop the daemon-owned
// notice, which comes before the profile blocks.
func TestWorkerPrompt_BackendSwitchNotice_RecoveryWithProfileInstructions(t *testing.T) {
	p := renderRecoveryPrompt(t, "PROFILE-INSTRUCTIONS previous={{ run.previous_backend }}", testSwitchNotice())
	assertNoticeOnce(t, p)
	assert.Less(t, strings.Index(p, backendSwitchNoticeHeading), strings.Index(p, "PROFILE-INSTRUCTIONS"))
	assert.Contains(t, p, "PROFILE-INSTRUCTIONS previous=claude")
}

// TestWorkerPrompt_BackendSwitchNotice_AbsentOnOrdinaryRun (CORE-101).
func TestWorkerPrompt_BackendSwitchNotice_AbsentOnOrdinaryRun(t *testing.T) {
	p := renderRecoveryPrompt(t, "", nil)
	assert.NotContains(t, p, backendSwitchNoticeHeading)
	assert.Contains(t, p, "## Prior Agent Handoffs")
}

// TestRunBindings_PreviousBackendProfileReasonResetsAt (CORE-101).
func TestRunBindings_PreviousBackendProfileReasonResetsAt(t *testing.T) {
	tmpl := "b={{ run.previous_backend }} p={{ run.previous_profile }} r={{ run.switch_reason }} t={{ run.limit_resets_at }}"
	p := renderRecoveryPrompt(t, tmpl, testSwitchNotice())
	assert.Contains(t, p, `b=claude p=claude-coder r=rate_limited automation "switch-to-codex" t=`+switchResetAt)

	plain := renderRecoveryPrompt(t, tmpl, nil)
	assert.Contains(t, plain, "b= p= r= t=", "an ordinary run binds empty strings")
}

// TestBackendSwitchNoticeFromStateAndTrigger (CORE-101): the event loop
// builds the notice from a rate_limited trigger, or from the persisted
// switch provenance (backend_fallback), with the prior backend's breaker
// reset when it is vendor-published.
func TestBackendSwitchNoticeFromStateAndTrigger(t *testing.T) {
	o := New(automationBaseCfg(), nil, nil, nil)
	state := NewState(o.cfg)
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	state.BackendHealth = map[string]BackendHealthEntry{
		"claude": {Backend: "claude", Status: "limited", LimitedUntil: reset, ResetKnown: true},
	}
	assert.Nil(t, o.backendSwitchNotice(&state, "ENG-1", "codex", "codex-coder", nil), "no switch, no notice")

	auto := &AutomationDispatch{AutomationID: "switch-to-codex", Trigger: AutomationTriggerContext{
		Type: config.AutomationTriggerRateLimited, FailedBackend: "claude", FailedProfile: "claude-coder",
	}}
	n := o.backendSwitchNotice(&state, "ENG-1", "codex", "codex-coder", auto)
	require.NotNil(t, n)
	assert.Equal(t, "claude", n.PreviousBackend)
	assert.Equal(t, "claude-coder", n.PreviousProfile)
	assert.Contains(t, n.Reason, "switch-to-codex")
	assert.True(t, n.LimitResetsAt.Equal(reset))

	state.AutoSwitchInfo = map[string]AutoSwitchRecord{"ENG-2": {
		Source: AutoSwitchSourceBackendFallback, FromBackend: "claude", FromProfile: "",
		ToBackend: "codex", Reason: "backend_fallback: claude limited", FromKey: "claude",
	}}
	n = o.backendSwitchNotice(&state, "ENG-2", "codex", "", nil)
	require.NotNil(t, n)
	assert.Equal(t, "claude", n.PreviousBackend)
	assert.Equal(t, "backend_fallback: claude limited", n.Reason)
	assert.Nil(t, o.backendSwitchNotice(&state, "ENG-2", "claude", "", nil), "the override no longer applies")
}
