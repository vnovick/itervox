package orchestrator

// CORE-033 — dispatch() must not hand a paused session id to a runner of a
// different backend (pause, switch the issue's profile to the other
// backend, resume → a Claude session id reaching `codex exec resume`, or
// the reverse). The comparison is against the backend MultiRunner will
// actually select for the resolved runner command; an empty stored backend
// (legacy disk entry) or a backend outside {claude, codex} is treated as a
// mismatch.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// sessionRecordingRunner reports the session id each RunTurn received.
type sessionRecordingRunner struct {
	seen chan *string
}

func (r *sessionRecordingRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	var copied *string
	if sessionID != nil {
		s := *sessionID
		copied = &s
	}
	r.seen <- copied
	return agent.TurnResult{}, nil
}

func TestDispatch_DropsCrossBackendResumeSession(t *testing.T) {
	cases := []struct {
		name           string
		command        string
		defaultBackend string
		storedBackend  string
		wantSession    bool
	}{
		{"same backend forwards the session", "claude", "", "claude", true},
		{"other backend drops the session", "claude", "", "codex", false},
		{"empty stored backend drops the session", "claude", "", "", false},
		{"unsupported resolved backend drops the session", "claude", "gemini", "gemini", false},
		{"codex to codex forwards the session", "codex", "", "codex", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg := automationBaseCfg()
			cfg.Agent.Command = tc.command
			cfg.Agent.Backend = tc.defaultBackend
			cfg.Agent.MaxTurns = 1
			issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
			mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
			runner := &sessionRecordingRunner{seen: make(chan *string, 4)}
			o := New(cfg, mt, runner, nil)

			state := NewState(cfg)
			state.PausedSessions[issue.Identifier] = &PausedSessionInfo{
				IssueID:   issue.ID,
				SessionID: "paused-session-1",
				Backend:   tc.storedBackend,
			}

			state = o.dispatch(ctx, state, issue, 0)
			assert.NotContains(t, state.PausedSessions, issue.Identifier, "the paused entry is consumed in every case")

			var got *string
			select {
			case got = <-runner.seen:
			case <-ctx.Done():
				t.Fatal("runner never called")
			}
			if tc.wantSession {
				require.NotNil(t, got, "same-backend resume must forward the session id")
				assert.Equal(t, "paused-session-1", *got)
			} else {
				assert.Nil(t, got, "cross-backend resume must start a fresh session")
			}

			cancel()
			o.workersWg.Wait(5 * time.Second)
		})
	}
}
