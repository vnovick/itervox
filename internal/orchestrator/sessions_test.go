package orchestrator_test

// #178 Sessions panel, steps 1–2, against a real orchestrator loop: the
// session list, a reply that resumes an input-required session, and
// operator messages delivered live (Claude) or on resume (Codex).

import (
	"context"
	"fmt"
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
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// sessionRunner is a fake runner keyed by issue: each issue's turns are
// scripted. It records every call.
type sessionRunner struct {
	mu    sync.Mutex
	calls []sessionCall
	turn  func(ctx context.Context, onProgress func(agent.TurnResult), call sessionCall) (agent.TurnResult, error)
}

type sessionCall struct {
	sessionID, prompt, workspace string
}

func (r *sessionRunner) RunTurn(ctx context.Context, _ agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	c := sessionCall{prompt: prompt, workspace: workspacePath}
	if sessionID != nil {
		c.sessionID = *sessionID
	}
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
	return r.turn(ctx, onProgress, c)
}

func (r *sessionRunner) recorded() []sessionCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sessionCall(nil), r.calls...)
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sessionFor(o *orchestrator.Orchestrator, identifier string) (orchestrator.AgentSession, bool) {
	for _, s := range orchestrator.Sessions(o.Snapshot(), false) {
		if s.Identifier == identifier {
			return s, true
		}
	}
	return orchestrator.AgentSession{}, false
}

func startLoop(t *testing.T, o *orchestrator.Orchestrator) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = o.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func sessionsConfig() *config.Config {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Work on {{ issue.identifier }}"
	return cfg
}

// TestSessionsListInputRequiredSessionsFirst: every running itervox run is a
// session keyed by the CLI's own session ID, owned by itervox; a session
// waiting for input sorts first even though it started later.
func TestSessionsListInputRequiredSessionsFirst(t *testing.T) {
	cfg := sessionsConfig()
	issues := []domain.Issue{
		makeIssue("id1", "ENG-1", "In Progress", prio(1), nil),
		makeIssue("id2", "ENG-2", "In Progress", prio(2), nil),
	}
	mt := tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	release := make(chan struct{})
	defer close(release)
	r := &sessionRunner{turn: func(ctx context.Context, onProgress func(agent.TurnResult), c sessionCall) (agent.TurnResult, error) {
		if strings.Contains(c.prompt, "ENG-1") {
			// Runs on: reports its session, then works until released.
			onProgress(agent.TurnResult{SessionID: "sess-running", LastText: "working"})
			select {
			case <-release:
			case <-ctx.Done():
			}
			return agent.TurnResult{Failed: true}, ctx.Err()
		}
		time.Sleep(50 * time.Millisecond) // starts after ENG-1
		return agent.TurnResult{SessionID: "sess-waiting", InputTokens: 3, OutputTokens: 2, InputRequired: true, FailureText: "Which database?"}, nil
	}}
	o := orchestrator.New(cfg, mt, r, nil)
	startLoop(t, o)

	waitFor(t, "one running and one waiting session", 5*time.Second, func() bool {
		s := orchestrator.Sessions(o.Snapshot(), false)
		return len(s) == 2 && s[0].Status == orchestrator.SessionStatusInputRequired && s[1].ID == "sess-running"
	})
	s := orchestrator.Sessions(o.Snapshot(), false)
	assert.Equal(t, "ENG-2", s[0].Identifier)
	assert.Equal(t, "sess-waiting", s[0].ID)
	assert.Equal(t, "Which database?", s[0].InputContext)
	assert.Empty(t, s[0].Steering, "a waiting session takes a reply, not a message")
	assert.Equal(t, "ENG-1", s[1].Identifier)
	assert.Equal(t, orchestrator.SessionStatusRunning, s[1].Status)
	assert.NotEmpty(t, s[1].RunID)
	assert.Equal(t, orchestrator.SteeringResume, s[1].Steering)
	for _, x := range s {
		assert.Equal(t, orchestrator.SessionOwnerItervox, x.Owner)
	}
}

// TestSessionReplyResumesInputRequiredAgent: the reply a session tab sends
// for a waiting session (provide-input, by the session's issue) resumes the
// same CLI session with the reply as its prompt.
func TestSessionReplyResumesInputRequiredAgent(t *testing.T) {
	cfg := sessionsConfig()
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	r := &sessionRunner{turn: func(_ context.Context, _ func(agent.TurnResult), c sessionCall) (agent.TurnResult, error) {
		if c.sessionID == "" {
			return agent.TurnResult{SessionID: "sess-1", InputTokens: 3, OutputTokens: 2, InputRequired: true, FailureText: "Which database?"}, nil
		}
		return agent.TurnResult{SessionID: "sess-1", InputTokens: 5, OutputTokens: 4, ResultText: "done"}, nil
	}}
	o := orchestrator.New(cfg, mt, r, nil)
	startLoop(t, o)

	var waiting orchestrator.AgentSession
	waitFor(t, "the session to wait for input", 5*time.Second, func() bool {
		s, ok := sessionFor(o, "ENG-1")
		waiting = s
		return ok && s.Status == orchestrator.SessionStatusInputRequired
	})
	require.Equal(t, "sess-1", waiting.ID)
	_, err := o.SendSessionMessage(waiting.ID, "hello")
	require.ErrorIs(t, err, orchestrator.ErrSessionInputRequired, "a waiting session takes a reply, not a message")

	require.NoError(t, o.ProvideInput(waiting.Identifier, "Use Postgres."))
	waitFor(t, "the resumed turn", 5*time.Second, func() bool { return len(r.recorded()) >= 2 })
	calls := r.recorded()
	assert.Equal(t, "sess-1", calls[1].sessionID, "the reply resumes the same CLI session")
	assert.Equal(t, "Use Postgres.", calls[1].prompt)
	waitFor(t, "the session to leave the waiting state", 5*time.Second, func() bool {
		s, ok := sessionFor(o, "ENG-1")
		return !ok || s.Status != orchestrator.SessionStatusInputRequired
	})
}

// TestCodexOperatorMessageIsQueuedAndDeliveredOnResume: Codex `exec` takes
// no input mid-run, so a message sent during the turn is queued and, once
// the turn ends, delivered as the prompt of a turn that resumes the session.
func TestCodexOperatorMessageIsQueuedAndDeliveredOnResume(t *testing.T) {
	cfg := sessionsConfig()
	cfg.Agent.Command = "codex"
	cfg.Agent.Backend = "codex"
	cfg.Agent.LiveSteering = true // ignored for Codex
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	release := make(chan struct{})
	r := &sessionRunner{turn: func(ctx context.Context, onProgress func(agent.TurnResult), c sessionCall) (agent.TurnResult, error) {
		if c.sessionID == "" {
			onProgress(agent.TurnResult{SessionID: "thread-1", LastText: "working"})
			select {
			case <-release:
			case <-ctx.Done():
				return agent.TurnResult{Failed: true}, ctx.Err()
			}
			return agent.TurnResult{SessionID: "thread-1", InputTokens: 3, OutputTokens: 2, ResultText: "first pass"}, nil
		}
		return agent.TurnResult{SessionID: "thread-1", InputTokens: 2, OutputTokens: 2, ResultText: "tests added"}, nil
	}}
	o := orchestrator.New(cfg, mt, r, nil)
	startLoop(t, o)

	waitFor(t, "the running session", 5*time.Second, func() bool {
		s, ok := sessionFor(o, "ENG-1")
		return ok && s.ID == "thread-1"
	})
	s, _ := sessionFor(o, "ENG-1")
	assert.Equal(t, orchestrator.SteeringResume, s.Steering, "Codex takes messages on resume")
	msg, err := o.SendSessionMessage("thread-1", "Also add tests.")
	require.NoError(t, err)
	waitFor(t, "the message to be queued", 5*time.Second, func() bool {
		s, _ := sessionFor(o, "ENG-1")
		return len(s.Messages) == 1 && s.Messages[0].Status == orchestrator.SessionMessageQueued
	})
	assert.Len(t, r.recorded(), 1, "nothing is delivered while the exec turn runs")

	close(release)
	waitFor(t, "the resumed turn", 5*time.Second, func() bool { return len(r.recorded()) >= 2 })
	calls := r.recorded()
	assert.Equal(t, "thread-1", calls[1].sessionID, "the message resumes the same session")
	assert.Contains(t, calls[1].prompt, "Also add tests.")
	assert.Contains(t, calls[1].prompt, "Operator message")
	waitFor(t, "the message to be marked delivered", 5*time.Second, func() bool {
		for _, m := range o.Snapshot().SessionMessages["ENG-1"] {
			if m.ID == msg.ID {
				return m.Status == orchestrator.SessionMessageDelivered && m.Delivery == orchestrator.SteeringResume
			}
		}
		return false
	})
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, r.recorded(), 2, "one resumed turn per batch of messages")
}

// streamingClaude is a fake claude in `--input-format stream-json` mode: it
// cannot finish its turn until a second user message (the operator's)
// arrives on its stdin.
const streamingClaude = `#!/bin/sh
printf '%%s\n' "$@" > %[1]s/argv
echo '{"type":"system","subtype":"init","session_id":"sess-live"}'
IFS= read -r first
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"working"}],"usage":{"input_tokens":3,"output_tokens":1}},"session_id":"sess-live"}'
IFS= read -r second
printf '%%s\n' "$second" > %[1]s/operator
echo '{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess-live","usage":{"input_tokens":2,"output_tokens":2}}'
cat > /dev/null
`

// TestOperatorMessageReachesRunningClaudeWorkerMidTurn: with
// agent.live_steering a message sent to a running local Claude session is
// written into the live turn's stdin — the real Claude runner against a
// fake CLI that cannot finish without it — and marked delivered live.
func TestOperatorMessageReachesRunningClaudeWorkerMidTurn(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(cli, fmt.Appendf(nil, streamingClaude, dir), 0o755))
	cfg := sessionsConfig()
	cfg.Agent.Command = cli
	cfg.Agent.LiveSteering = true
	cfg.Agent.ReadTimeoutMs = 10_000
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := orchestrator.New(cfg, mt, agent.NewClaudeRunner(), &stableWorkspaceProvider{path: filepath.Join(dir, "ws")})
	startLoop(t, o)

	waitFor(t, "the live session", 5*time.Second, func() bool {
		s, ok := sessionFor(o, "ENG-1")
		return ok && s.ID == "sess-live"
	})
	s := orchestrator.Sessions(o.Snapshot(), true)[0]
	assert.Equal(t, orchestrator.SteeringLive, s.Steering)
	msg, err := o.SendSessionMessage("sess-live", "Stop and summarise.")
	require.NoError(t, err)

	waitFor(t, "the operator message on the CLI's stdin", 10*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(dir, "operator"))
		return err == nil && strings.Contains(string(b), "Stop and summarise.")
	})
	waitFor(t, "the message to be marked delivered live", 5*time.Second, func() bool {
		for _, m := range o.Snapshot().SessionMessages["ENG-1"] {
			if m.ID == msg.ID {
				return m.Status == orchestrator.SessionMessageDelivered && m.Delivery == orchestrator.SteeringLive
			}
		}
		return false
	})
	argv, err := os.ReadFile(filepath.Join(dir, "argv"))
	require.NoError(t, err)
	assert.Contains(t, string(argv), "--input-format\nstream-json\n")
	waitFor(t, "the run to finish", 10*time.Second, func() bool {
		_, running := sessionFor(o, "ENG-1")
		return !running
	})
}

// TestSessionMessageTargetsAreClassified: a message needs a running session
// itervox owns and a non-empty body of at most 10 KiB.
func TestSessionMessageTargetsAreClassified(t *testing.T) {
	o := orchestrator.New(baseConfig(), tracker.NewMemoryTracker(nil, nil, nil), &sessionRunner{}, nil)
	_, err := o.SendSessionMessage("nope", "hi")
	assert.ErrorIs(t, err, orchestrator.ErrSessionNotFound)
	_, err = o.SendSessionMessage("nope", "   ")
	assert.ErrorIs(t, err, orchestrator.ErrInvalidSessionMessage)
	_, err = o.SendSessionMessage("nope", strings.Repeat("x", orchestrator.MaxSessionMessageBytes+1))
	assert.ErrorIs(t, err, orchestrator.ErrInvalidSessionMessage)
}
