package orchestrator

// Quota pools: a profile with pool: local runs the claude backend but has its
// own circuit breaker ("claude/local"), and agent.backend_fallback can name
// the lane as a chain step.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

const localClaudeCommand = "ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude --model local"

// pooledOrchestrator is breakerOrchestrator plus coder-local, a claude
// profile pointed at a local model server in pool "local".
func pooledOrchestrator(t *testing.T, fb config.BackendFallbackConfig, issues ...domain.Issue) (*Orchestrator, *recordingRunner) {
	t.Helper()
	o, rr := breakerOrchestrator(t, fb, issues...)
	o.cfgMu.Lock()
	o.cfg.Agent.Profiles["coder-local"] = config.AgentProfile{Command: localClaudeCommand, Pool: "local"}
	o.cfgMu.Unlock()
	return o, rr
}

func localRuns(rr *recordingRunner) int {
	n := 0
	for _, c := range rr.snapshot() {
		if strings.Contains(c, "127.0.0.1:8000") {
			n++
		}
	}
	return n
}

// TestPooledProfileHasItsOwnBreaker: a claude usage limit does not hold a
// pool: local claude profile, and a limit on the pooled lane does not hold
// plain claude.
func TestPooledProfileHasItsOwnBreaker(t *testing.T) {
	t.Run("claude limited, pooled profile still dispatches", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, b := breakerIssue("1"), breakerIssue("2")
		o, rr := pooledOrchestrator(t, config.BackendFallbackConfig{}, a, b)
		state := NewState(o.cfg)
		state.IssueProfiles[a.Identifier] = "coder"
		state.IssueProfiles[b.Identifier] = "coder-local"

		state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
		require.Contains(t, state.BackendHealth, "claude")
		require.NotContains(t, state.BackendHealth, "claude/local")

		state = o.dispatch(ctx, state, b, 0)
		require.Contains(t, state.Running, b.ID, "the pooled profile is not held by the claude breaker")
		run := state.Running[b.ID]
		assert.Equal(t, "claude", run.Backend, "the runner is still selected by the plain backend")
		assert.Equal(t, "local", run.Pool)
		assert.NotContains(t, state.BackendLimitedHolds, b.Identifier)
		deadline := time.Now().Add(5 * time.Second)
		for localRuns(rr) < 1 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		assert.Equal(t, 1, localRuns(rr), "the pooled profile's own command ran: %v", rr.snapshot())
		cancel()
	})
	t.Run("pooled lane limited, plain claude still dispatches", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, b, c := breakerIssue("1"), breakerIssue("2"), breakerIssue("3")
		o, _ := pooledOrchestrator(t, config.BackendFallbackConfig{}, a, b, c)
		state := NewState(o.cfg)
		state.IssueProfiles[a.Identifier] = "coder"
		state.IssueProfiles[b.Identifier] = "coder-local"
		state.IssueProfiles[c.Identifier] = "coder-local"

		// b runs through the real dispatch path, so its RunEntry carries the
		// pool, and its limit exit opens the pooled breaker.
		state = o.dispatch(ctx, state, b, 0)
		require.Contains(t, state.Running, b.ID)
		require.Equal(t, "local", state.Running[b.ID].Pool)
		state = limitExit(ctx, o, state, b, "coder-local", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
		require.Contains(t, state.BackendHealth, "claude/local")
		assert.NotContains(t, state.BackendHealth, "claude", "the plain claude breaker stays closed")
		e := state.BackendHealth["claude/local"]
		assert.Equal(t, "claude", e.Backend)
		assert.Equal(t, "local", e.Pool)

		state = o.dispatch(ctx, state, a, 0)
		require.Contains(t, state.Running, a.ID, "plain claude is not held by the pooled breaker")
		assert.Empty(t, state.Running[a.ID].Pool)

		state = o.dispatch(ctx, state, c, 0)
		assert.NotContains(t, state.Running, c.ID, "another pooled issue is held")
		assert.Equal(t, "claude/local", state.BackendLimitedHolds[c.Identifier].Key)
		assert.Equal(t, IneligibleBackendLimited, IneligibleReason(c, state, o.cfg))
		cancel()
	})
	t.Run("pending input resume uses the pooled breaker", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a := breakerIssue("1")
		o, _ := pooledOrchestrator(t, config.BackendFallbackConfig{}, a)
		state := NewState(o.cfg)
		now := time.Now()
		o.recordBackendLimit(&state, "claude", "", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)
		state.PendingInputResumes[a.Identifier] = &PendingInputResumeEntry{
			IssueID: a.ID, Identifier: a.Identifier, SessionID: "sess-1", Backend: "claude",
			Command: localClaudeCommand, ProfileName: "coder-local", UserMessage: "yes",
		}
		o.recordBackendLimit(&state, "claude", "local", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)
		state = o.processPendingInputResumes(ctx, state, now)
		assert.NotContains(t, state.Running, a.ID, "a pooled session waits for its own breaker")
		assert.Equal(t, "claude/local", state.BackendLimitedHolds[a.Identifier].Key)

		delete(state.BackendHealth, "claude/local")
		state = o.processPendingInputResumes(ctx, state, time.Now())
		require.Contains(t, state.Running, a.ID, "the claude breaker does not hold the pooled session")
		assert.Equal(t, "local", state.Running[a.ID].Pool)
		cancel()
	})
}

// TestBackendFallbackChainReroutesToPool: chain [claude, claude/local] with
// a profile_map entry reroutes a limited claude run to the pooled profile,
// and switch_back at_reset returns it to claude.
func TestBackendFallbackChainReroutesToPool(t *testing.T) {
	pooledFallback := func(chain ...string) config.BackendFallbackConfig {
		fb := fallbackOn()
		fb.Chain = chain
		fb.ProfileMap = map[string]map[string]string{
			"coder": {"codex": "coder-codex", "claude/local": "coder-local"},
		}
		return fb
	}
	t.Run("claude limited reroutes to claude/local and switches back at reset", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a := breakerIssue("1")
		o, rr := pooledOrchestrator(t, pooledFallback("claude", "claude/local"), a)
		require.NoError(t, config.ValidateBackendFallback(o.cfg.Agent.BackendFallback, o.cfg.Agent.Profiles, o.cfg.Agent.Command))
		state := NewState(o.cfg)
		state.IssueProfiles[a.Identifier] = "coder"

		state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(5*time.Hour)))
		require.Contains(t, state.Running, a.ID, "the limited claude run is rerouted, not held")
		run := state.Running[a.ID]
		assert.Equal(t, "coder-local", run.ProfileName)
		assert.Equal(t, "claude", run.Backend)
		assert.Equal(t, "local", run.Pool)
		assert.Equal(t, "coder-local", state.IssueProfiles[a.Identifier])
		assert.Equal(t, "claude", state.IssueBackends[a.Identifier], "the override keeps the runner backend, not the lane")
		rec := state.AutoSwitchInfo[a.Identifier]
		assert.Equal(t, AutoSwitchSourceBackendFallback, rec.Source)
		assert.Equal(t, "claude", rec.FromKey)
		assert.NotContains(t, state.BackendHealth, "claude/local")
		deadline := time.Now().Add(5 * time.Second)
		for localRuns(rr) < 1 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		require.Equal(t, 1, localRuns(rr), "the pooled profile's command ran: %v", rr.snapshot())
		switchedAt := rec.SwitchedAt

		// The pooled run succeeds while claude is still limited: at_reset
		// keeps the override.
		attempt := 0
		state = o.handleEvent(ctx, state, OrchestratorEvent{
			Type: EventWorkerExited, IssueID: a.ID,
			RunEntry: &RunEntry{Issue: a, TerminalReason: TerminalSucceeded, RetryAttempt: &attempt},
		})
		assert.Equal(t, "coder-local", state.IssueProfiles[a.Identifier], "no flap on success")
		assert.Zero(t, o.revertBackendFallbackSwitches(&state, switchedAt.Add(31*time.Minute)),
			"dwell elapsed but claude has not reset: no switch back")
		state = o.dispatch(ctx, state, a, 0)
		require.Contains(t, state.Running, a.ID)
		assert.Equal(t, "local", state.Running[a.ID].Pool, "the issue stays on the pooled lane")
		delete(state.Running, a.ID)
		delete(state.Claimed, a.ID)

		// Claude's reset passes: still inside the dwell no switch back, then
		// the dwell elapses and it switches back.
		e := state.BackendHealth["claude"]
		e.LimitedUntil = time.Now().Add(-time.Second)
		state.BackendHealth["claude"] = e
		assert.Zero(t, o.revertBackendFallbackSwitches(&state, switchedAt.Add(29*time.Minute)), "inside the dwell")
		assert.Equal(t, 1, o.revertBackendFallbackSwitches(&state, switchedAt.Add(31*time.Minute)))
		assert.NotContains(t, state.AutoSwitchInfo, a.Identifier)
		assert.NotContains(t, state.IssueBackends, a.Identifier)
		state = o.dispatch(ctx, state, a, 0)
		require.Contains(t, state.Running, a.ID)
		assert.Equal(t, "claude", state.Running[a.ID].Backend)
		assert.Empty(t, state.Running[a.ID].Pool, "back on plain claude")
		assert.Equal(t, BackendStatusProbing, state.BackendHealth["claude"].Status)
		assert.Equal(t, a.Identifier, state.BackendHealth["claude"].ProbeIssue, "the switch-back run is claude's probe")
		cancel()
	})
	t.Run("three-step chain skips a limited codex", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a := breakerIssue("1")
		o, _ := pooledOrchestrator(t, pooledFallback("claude", "codex", "claude/local"), a)
		require.NoError(t, config.ValidateBackendFallback(o.cfg.Agent.BackendFallback, o.cfg.Agent.Profiles, o.cfg.Agent.Command))
		state := NewState(o.cfg)
		state.IssueProfiles[a.Identifier] = "coder"
		now := time.Now()
		o.recordBackendLimit(&state, "codex", "", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)

		state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(now.Add(time.Hour)))
		require.Contains(t, state.Running, a.ID)
		assert.Equal(t, "coder-local", state.Running[a.ID].ProfileName)
		assert.Equal(t, "local", state.Running[a.ID].Pool)
		cancel()
	})
	t.Run("pooled lane limited too: held on the earliest breaker", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a := breakerIssue("1")
		o, _ := pooledOrchestrator(t, pooledFallback("claude", "claude/local"), a)
		state := NewState(o.cfg)
		state.IssueProfiles[a.Identifier] = "coder"
		now := time.Now()
		o.recordBackendLimit(&state, "claude", "local", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)

		state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(now.Add(2*time.Hour)))
		assert.NotContains(t, state.Running, a.ID)
		assert.Equal(t, "claude/local", state.BackendLimitedHolds[a.Identifier].Key, "held on the lane that resets first")
		cancel()
	})
}

// TestUnpooledBreakerKeysUnchanged guards the historical keys: without a
// pool every key, entry and persisted record is what it was before pools.
func TestUnpooledBreakerKeysUnchanged(t *testing.T) {
	assert.Equal(t, "claude", BackendHealthKey("claude", "", ""))
	assert.Equal(t, "codex@build-1", BackendHealthKey("codex", "", "build-1"))
	assert.Equal(t, "claude/local", BackendHealthKey("claude", "local", ""))
	assert.Equal(t, "claude/local@build-1", BackendHealthKey("claude", "local", "build-1"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	path := filepath.Join(t.TempDir(), "backend_health.json")
	o, _ := pooledOrchestrator(t, config.BackendFallbackConfig{}, a)
	o.SetBackendHealthFile(path)
	state := NewState(o.cfg)
	state.IssueProfiles[a.Identifier] = "coder"
	state = o.dispatch(ctx, state, a, 0)
	require.Contains(t, state.Running, a.ID)
	assert.Empty(t, state.Running[a.ID].Pool, "an unpooled profile's run carries no pool")

	now := time.Now()
	reset := now.Add(time.Hour).Truncate(time.Second)
	require.True(t, o.recordBackendLimit(&state, "claude", "", "", "ENG-1", quotaLimit(reset), now))
	require.True(t, o.recordBackendLimit(&state, "codex", "", "build-1", "ENG-1", quotaLimit(reset), now))
	keys := make([]string, 0, len(state.BackendHealth))
	for k := range state.BackendHealth {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"claude", "codex@build-1"}, keys)
	o.saveBackendHealthToDisk(&state)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"pool"`, "an unpooled breaker persists no pool key")
	var env struct {
		Version  int                       `json:"version"`
		Backends map[string]map[string]any `json:"backends"`
	}
	require.NoError(t, json.Unmarshal(data, &env))
	assert.Equal(t, 1, env.Version)
	assert.Equal(t, "claude", env.Backends["claude"]["backend"])
	assert.Equal(t, "build-1", env.Backends["codex@build-1"]["host"])
}

// TestPooledBreakerClearAndPrune: the operator clear takes the lane the
// snapshot reports, and orphan pruning is per lane.
func TestPooledBreakerClearAndPrune(t *testing.T) {
	o, _ := pooledOrchestrator(t, config.BackendFallbackConfig{})
	require.True(t, o.ClearBackendBreaker("claude/local", "build-1"))
	ev := <-o.events
	assert.Equal(t, EventClearBackendBreaker, ev.Type)
	assert.Equal(t, "claude/local@build-1", ev.Identifier)

	state := NewState(o.cfg)
	for _, pool := range []string{"local", "gone"} {
		state.BackendHealth[BackendHealthKey("claude", pool, "")] = BackendHealthEntry{
			Backend: "claude", Pool: pool, Status: BackendStatusProbing,
		}
	}
	require.True(t, o.pruneOrphanBreakers(&state))
	assert.Contains(t, state.BackendHealth, "claude/local", "coder-local still runs the pool")
	assert.NotContains(t, state.BackendHealth, "claude/gone", "no profile or chain step runs the pool any more")
}

// TestResolveDispatchTargetCarriesPool: the resolver carries the profile's
// pool next to Backend (never inside it), and drops it when a per-issue or
// recovery request moves the run to another backend.
func TestResolveDispatchTargetCarriesPool(t *testing.T) {
	local := config.AgentProfile{Command: localClaudeCommand, Pool: "local"}
	got := resolveDispatchTarget(dispatchTargetInput{DefaultCommand: "claude", Profile: &local})
	assert.Equal(t, "claude", got.Backend, "the runner backend stays claude")
	assert.Equal(t, "local", got.Pool)
	assert.Equal(t, "claude/local", got.Lane())

	same := resolveDispatchTarget(dispatchTargetInput{DefaultCommand: "claude", Profile: &local, IssueBackend: "claude"})
	assert.Equal(t, "claude/local", same.Lane(), "a request for the same backend keeps the pool")

	wrapper := config.AgentProfile{Command: "./run-agent.sh", Backend: "claude", Pool: "local"}
	moved := resolveDispatchTarget(dispatchTargetInput{DefaultCommand: "claude", Profile: &wrapper, IssueBackend: "codex"})
	assert.Equal(t, "codex", moved.Backend)
	assert.Empty(t, moved.Pool, "the pool names a quota of the backend the run left")

	plain := resolveDispatchTarget(dispatchTargetInput{DefaultCommand: "claude"})
	assert.Equal(t, "claude", plain.Lane())
	assert.Empty(t, plain.Pool)
}
