package orchestrator_test

// #178 step 3 against a real orchestrator loop: sessions started by hand
// are listed read-only; "Continue in itervox" is refused while the original
// process may still run, and otherwise resumes the session under itervox
// with the CLI's own resume arguments, after which operator messages reach
// it.

import (
	"context"
	"errors"
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
	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// fakeExternalSource serves a fixed discovery list and per-session liveness.
type fakeExternalSource struct {
	mu        sync.Mutex
	sessions  []extsessions.Session
	running   map[string]string // id -> reason
	livenessN int
}

func (f *fakeExternalSource) Discover(time.Time) ([]extsessions.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]extsessions.Session(nil), f.sessions...)
	for i := range out {
		if reason, ok := f.running[out[i].ID]; ok {
			out[i].Running, out[i].RunningReason = true, reason
		}
	}
	return out, nil
}

func (f *fakeExternalSource) Liveness(s extsessions.Session, _ time.Time) extsessions.Liveness {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.livenessN++
	if reason, ok := f.running[s.ID]; ok {
		return extsessions.Liveness{Running: true, Reason: reason}
	}
	return extsessions.Liveness{Certain: true, Reason: "no claude process"}
}

func (f *fakeExternalSource) setRunning(id, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if reason == "" {
		delete(f.running, id)
		return
	}
	f.running[id] = reason
}

// argsRunner records every argument of RunTurn.
type argsRunner struct {
	mu    sync.Mutex
	calls []argsCall
	block chan struct{} // when non-nil, turns wait for it
}

type argsCall struct {
	SessionID, Prompt, Workspace, Command, WorkerHost string
	Permission                                        agent.PermissionMode
}

func (r *argsRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, _ string, _, _ int, mode agent.PermissionMode) (agent.TurnResult, error) {
	c := argsCall{Prompt: prompt, Workspace: workspacePath, Command: command, WorkerHost: workerHost, Permission: mode}
	if sessionID != nil {
		c.SessionID = *sessionID
	}
	r.mu.Lock()
	r.calls = append(r.calls, c)
	block := r.block
	r.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return agent.TurnResult{}, ctx.Err()
		}
	}
	return agent.TurnResult{SessionID: c.SessionID, LastText: "done: " + prompt}, nil
}

func (r *argsRunner) recorded() []argsCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]argsCall(nil), r.calls...)
}

func extSession(id, backend, cwd string) extsessions.Session {
	return extsessions.Session{ID: id, Backend: backend, Cwd: cwd, Path: filepath.Join(cwd, id+".jsonl"),
		Title: "hand " + id, UpdatedAt: time.Now().Add(-time.Hour)}
}

func sessionByID(o *orchestrator.Orchestrator, id string) (orchestrator.AgentSession, bool) {
	for _, s := range orchestrator.Sessions(o.Snapshot(), false) {
		if s.ID == id {
			return s, true
		}
	}
	return orchestrator.AgentSession{}, false
}

func externalLoop(t *testing.T, r agent.Runner, src *fakeExternalSource) *orchestrator.Orchestrator {
	t.Helper()
	cfg := sessionsConfig()
	cfg.Agent.Command = "claude"
	cfg.Agent.ReadTimeoutMs, cfg.Agent.TurnTimeoutMs = 30000, 60000
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := orchestrator.New(cfg, mt, r, nil)
	o.SetExternalSessionSource(src, 20*time.Millisecond)
	startLoop(t, o)
	return o
}

// Sessions started by hand appear read-only (owner external), with their
// directory; one still running says why; messaging one is refused.
func TestExternalSessionsListedReadOnly(t *testing.T) {
	src := &fakeExternalSource{
		sessions: []extsessions.Session{extSession("ext-exited", "claude", "/work/app"), extSession("ext-busy", "codex", "/work/app/web")},
		running:  map[string]string{"ext-busy": "codex process 42 has the transcript open"},
	}
	o := externalLoop(t, &argsRunner{}, src)
	waitFor(t, "discovery", 2*time.Second, func() bool {
		_, ok := sessionByID(o, "ext-busy")
		return ok
	})
	exited, _ := sessionByID(o, "ext-exited")
	busy, _ := sessionByID(o, "ext-busy")
	assert.Equal(t, orchestrator.SessionOwnerExternal, exited.Owner)
	assert.Equal(t, orchestrator.SessionStatusExited, exited.Status)
	assert.Equal(t, "/work/app", exited.Cwd)
	assert.Equal(t, orchestrator.SessionOwnerExternal, busy.Owner)
	assert.Equal(t, orchestrator.SessionStatusRunning, busy.Status)
	assert.Contains(t, busy.InUseReason, "transcript open")
	assert.Empty(t, exited.Steering, "a session itervox does not own takes no message")

	_, err := o.SendSessionMessage("ext-exited", "hello")
	assert.ErrorIs(t, err, orchestrator.ErrSessionNotOwned)
}

// "Continue in itervox" is refused while the original process may still be
// running — checked again at the moment of the request, not only from the
// last discovery pass — and nothing is started.
func TestContinueRefusedWhileOriginalProcessRuns(t *testing.T) {
	src := &fakeExternalSource{sessions: []extsessions.Session{extSession("ext-1", "claude", "/work/app")}, running: map[string]string{}}
	r := &argsRunner{}
	o := externalLoop(t, r, src)
	waitFor(t, "discovery", 2*time.Second, func() bool { _, ok := sessionByID(o, "ext-1"); return ok })

	// Discovery last saw it exited; the process started again since.
	src.setRunning("ext-1", "a claude process (77) is running in the session's directory; it may be this session")
	_, err := o.ContinueExternalSession("ext-1", "", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, orchestrator.ErrSessionInUse)
	var inUse *orchestrator.SessionInUseError
	require.True(t, errors.As(err, &inUse))
	assert.Contains(t, inUse.Reason, "process (77)")
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, r.recorded(), "no turn may start while the session may be in use")
	s, _ := sessionByID(o, "ext-1")
	assert.Equal(t, orchestrator.SessionOwnerExternal, s.Owner)

	_, err = o.ContinueExternalSession("nope", "", "")
	assert.ErrorIs(t, err, orchestrator.ErrSessionNotFound)
}

// Once the original process has exited, "Continue in itervox" resumes the
// session under itervox: a manual run with no issue, in the session's own
// directory, resuming the CLI's session ID with the operator's prompt and
// the confining permission mode. The session is then owned (no longer
// listed as external), and a message sent to it runs the next resumed turn;
// a second continue is refused.
func TestContinueResumesExitedSessionUnderItervox(t *testing.T) {
	src := &fakeExternalSource{sessions: []extsessions.Session{
		extSession("claude-1", "claude", "/work/app"), extSession("codex-1", "codex", "/work/app/svc"),
	}, running: map[string]string{}}
	r := &argsRunner{}
	o := externalLoop(t, r, src)
	waitFor(t, "discovery", 2*time.Second, func() bool { _, ok := sessionByID(o, "codex-1"); return ok })

	row, err := o.ContinueExternalSession("claude-1", "Finish the migration", "")
	require.NoError(t, err)
	assert.Equal(t, orchestrator.SessionOwnerItervox, row.Owner)
	assert.Equal(t, orchestrator.SessionKindManual, row.Kind)
	waitFor(t, "the resumed turn", 2*time.Second, func() bool { return len(r.recorded()) == 1 })
	c := r.recorded()[0]
	assert.Equal(t, argsCall{SessionID: "claude-1", Prompt: "Finish the migration", Workspace: "/work/app",
		Command: agent.CommandWithBackendHint("claude", "claude"), Permission: agent.PermissionSandbox}, c)

	waitFor(t, "the turn to finish", 2*time.Second, func() bool {
		s, _ := sessionByID(o, "claude-1")
		return s.Status == orchestrator.SessionStatusIdle
	})
	s, _ := sessionByID(o, "claude-1")
	assert.Equal(t, orchestrator.SessionOwnerItervox, s.Owner, "continued sessions are owned")
	assert.Equal(t, "done: Finish the migration", s.LastEvent)
	assert.Equal(t, 1, s.TurnCount)
	// Discovery keeps finding it on disk, but it is no longer external.
	time.Sleep(60 * time.Millisecond)
	for _, x := range orchestrator.Sessions(o.Snapshot(), false) {
		if x.ID == "claude-1" {
			assert.Equal(t, orchestrator.SessionOwnerItervox, x.Owner)
		}
	}

	// The composer works: a message runs the next resumed turn.
	msg, err := o.SendSessionMessage("claude-1", "Also update the docs")
	require.NoError(t, err)
	waitFor(t, "the message's turn", 2*time.Second, func() bool { return len(r.recorded()) == 2 })
	assert.Equal(t, "Also update the docs", r.recorded()[1].Prompt)
	assert.Equal(t, "claude-1", r.recorded()[1].SessionID)
	waitFor(t, "delivery", 2*time.Second, func() bool {
		s, _ := sessionByID(o, "claude-1")
		return len(s.Messages) == 1 && s.Messages[0].ID == msg.ID && s.Messages[0].Status == orchestrator.SessionMessageDelivered
	})

	_, err = o.ContinueExternalSession("claude-1", "", "")
	assert.ErrorIs(t, err, orchestrator.ErrSessionAlreadyOwned)

	// Codex: the backend's own binary resumes its session; the default
	// prompt is used when the operator gave none.
	_, err = o.ContinueExternalSession("codex-1", "", "")
	require.NoError(t, err)
	waitFor(t, "the codex turn", 2*time.Second, func() bool { return len(r.recorded()) == 3 })
	cx := r.recorded()[2]
	assert.Equal(t, "codex-1", cx.SessionID)
	assert.Equal(t, "/work/app/svc", cx.Workspace)
	assert.Equal(t, agent.CommandWithBackendHint("codex", "codex"), cx.Command)
	assert.Equal(t, "Continue where you left off.", cx.Prompt)
}

// Messages sent while a continued session's turn runs are queued and become
// the next turn's prompt.
func TestContinuedSessionQueuesMessagesWhileATurnRuns(t *testing.T) {
	src := &fakeExternalSource{sessions: []extsessions.Session{extSession("c-1", "claude", "/work/app")}, running: map[string]string{}}
	block := make(chan struct{})
	r := &argsRunner{block: block}
	o := externalLoop(t, r, src)
	waitFor(t, "discovery", 2*time.Second, func() bool { _, ok := sessionByID(o, "c-1"); return ok })
	_, err := o.ContinueExternalSession("c-1", "first", "")
	require.NoError(t, err)
	waitFor(t, "the first turn", 2*time.Second, func() bool { return len(r.recorded()) == 1 })
	_, err = o.SendSessionMessage("c-1", "second")
	require.NoError(t, err)
	_, err = o.SendSessionMessage("c-1", "third")
	require.NoError(t, err)
	waitFor(t, "queued messages", 2*time.Second, func() bool {
		s, _ := sessionByID(o, "c-1")
		return len(s.Messages) == 2 && s.Messages[1].Status == orchestrator.SessionMessageQueued
	})
	close(block)
	waitFor(t, "the queued turn", 2*time.Second, func() bool { return len(r.recorded()) == 2 })
	assert.Equal(t, "second\n\nthird", r.recorded()[1].Prompt)
}

// The continued run uses the real runners: claude is resumed with
// `--resume <id>` and codex with `exec resume <id>`, each in the session's
// own directory.
func TestContinueInItervoxResumesWithTheCLIResumeArgs(t *testing.T) {
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args.log")
	claudeOut := `{"type":"system","subtype":"init","session_id":"hand-claude"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"hand-claude","usage":{"input_tokens":1,"output_tokens":1}}`
	codexOut := `{"type":"thread.started","thread_id":"hand-codex"}` + "\n" +
		`{"type":"item.completed","item":{"id":"i0","type":"agent_message","text":"ok"}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`
	for name, out := range map[string]string{"claude": claudeOut, "codex": codexOut} {
		script := fmt.Sprintf("#!/bin/sh\necho \"%s cwd=$(pwd) args=$*\" >> '%s'\ncat > /dev/null\nprintf '%%s\\n' '%s'\n", name, argsFile, out)
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755))
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	claudeDir, codexDir := t.TempDir(), t.TempDir()
	src := &fakeExternalSource{sessions: []extsessions.Session{
		extSession("hand-claude", "claude", claudeDir), extSession("hand-codex", "codex", codexDir),
	}, running: map[string]string{}}
	runner := agent.NewMultiRunner(agent.NewClaudeRunner(), map[string]agent.Runner{"codex": agent.NewCodexRunner()})
	o := externalLoop(t, runner, src)
	waitFor(t, "discovery", 2*time.Second, func() bool { _, ok := sessionByID(o, "hand-codex"); return ok })
	_, err := o.ContinueExternalSession("hand-claude", "go on", "")
	require.NoError(t, err)
	_, err = o.ContinueExternalSession("hand-codex", "go on", "")
	require.NoError(t, err)
	waitFor(t, "both turns", 10*time.Second, func() bool {
		for _, id := range []string{"hand-claude", "hand-codex"} {
			s, _ := sessionByID(o, id)
			if s.Status != orchestrator.SessionStatusIdle {
				return false
			}
		}
		return true
	})
	for _, id := range []string{"hand-claude", "hand-codex"} {
		s, _ := sessionByID(o, id)
		require.Empty(t, s.LastError, id)
	}
	raw, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	t.Logf("CLI invocations:\n%s", raw)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 2, "args log: %s", raw)
	var claudeLine, codexLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "claude ") {
			claudeLine = l
		} else {
			codexLine = l
		}
	}
	realClaudeDir, _ := filepath.EvalSymlinks(claudeDir)
	assert.Contains(t, claudeLine, "cwd="+realClaudeDir)
	assert.Contains(t, claudeLine, "--resume hand-claude")
	assert.Contains(t, claudeLine, "--permission-mode acceptEdits", "a continued session defaults to the confining mode")
	assert.Contains(t, codexLine, "exec resume")
	assert.Contains(t, codexLine, "hand-codex")
	assert.Contains(t, codexLine, codexDir)
	for _, id := range []string{"hand-claude", "hand-codex"} {
		s, _ := sessionByID(o, id)
		assert.Empty(t, s.LastError, id)
	}
}
