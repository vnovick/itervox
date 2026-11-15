package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tmuxctl"
)

// Re-adoption after a restart and stopping sessions (#178), end to end on
// real tmux (private socket) with shell stand-ins for the agent CLIs.

// startSession starts a session through POST /api/v1/sessions.
func (r *tmuxRig) startSession() server.SessionRow {
	r.t.Helper()
	w := r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"claude"}`)
	require.Equal(r.t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		Session server.SessionRow `json:"session"`
	}
	require.NoError(r.t, json.Unmarshal(w.Body.Bytes(), &created))
	return created.Session
}

// stopSession posts a stop and decodes the result.
func (r *tmuxRig) stopSession(id, body string) (int, server.SessionStopResult, string) {
	r.t.Helper()
	w := r.do(http.MethodPost, "/api/v1/sessions/"+id+"/stop", body)
	var res server.SessionStopResult
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	return w.Code, res, w.Body.String()
}

func (r *tmuxRig) tmuxHas(name string) bool {
	r.t.Helper()
	has, err := r.tmux.HasSession(context.Background(), name)
	require.NoError(r.t, err)
	return has
}

// stubbornCLI writes a `claude` stand-in that ignores Ctrl-C (SIGINT) and
// makes it the rig's agent command.
func (r *tmuxRig) stubbornCLI() string {
	r.t.Helper()
	dir := filepath.Join(filepath.Dir(r.bin), "stubborn")
	require.NoError(r.t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "claude")
	require.NoError(r.t, os.WriteFile(path, []byte("#!/bin/sh\ntrap '' INT\necho ready\nwhile :; do sleep 1; done\n"), 0o755))
	r.ts.agentCommand = path
	return path
}

// A session started with "New session" is tagged with this project; after
// a daemon restart the new daemon re-adopts it as its own (controllable,
// attach command, Stop), and an untagged same-named session and one tagged
// by another project are never adopted.
func TestRestartReadoptsOnlyThisProjectsTmuxSessions(t *testing.T) {
	r := newTmuxRig(t, "", true)
	started := r.startSession()
	r.waitRow(started.ID, func(s server.SessionRow) bool { return s.TmuxPane != nil })
	ctx := context.Background()
	other := t.TempDir()
	require.NoError(t, r.tmux.NewSession(ctx, "itervox-aaaaaa", r.project, filepath.Join(r.bin, "claude")))
	require.NoError(t, r.tmux.SetSessionOptions(ctx, "itervox-aaaaaa",
		map[string]string{tmuxctl.OptProject: other, tmuxctl.OptBackend: "claude"}))
	require.NoError(t, r.tmux.NewSession(ctx, "itervox-bbbbbb", r.project, filepath.Join(r.bin, "claude")))

	r.boot() // the daemon restarts; tmux keeps running
	_, tracked := r.orch.FindTmuxSession(started.ID)
	assert.False(t, tracked, "a fresh daemon starts with nothing")

	row := r.waitRow(started.ID, func(s server.SessionRow) bool { return s.Adopted })
	assert.Equal(t, server.SessionOwnerItervox, row.Owner)
	assert.Equal(t, "tmux", row.Kind)
	assert.True(t, row.Controllable)
	assert.Equal(t, r.ts.attachCommand(started.ID), row.AttachCommand)
	assert.Equal(t, r.project, row.Cwd)
	assert.False(t, row.GuardrailsStale, "it was started without guardrails")

	time.Sleep(200 * time.Millisecond) // several more discovery passes
	for _, name := range []string{"itervox-aaaaaa", "itervox-bbbbbb"} {
		_, owned := r.orch.FindTmuxSession(name)
		assert.False(t, owned, "%s is not this project's", name)
		code, _, body := r.stopSession(name, `{"force":true}`)
		assert.Equal(t, http.StatusNotFound, code, body)
		assert.True(t, r.tmuxHas(name), "%s is untouched", name)
	}

	code, res, body := r.stopSession(started.ID, "")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "stopped", res.Outcome)
	assert.False(t, r.tmuxHas(started.ID))
}

// A guarded session's tag carries over: the adopted row says its
// guardrails come from the previous daemon process.
func TestAdoptedGuardedSessionIsMarkedStale(t *testing.T) {
	r := newTmuxRig(t, "", true)
	ctx := context.Background()
	id := "itervox-0abc12"
	require.NoError(t, r.tmux.NewSession(ctx, id, r.project, filepath.Join(r.bin, "claude")))
	require.NoError(t, r.tmux.SetSessionOptions(ctx, id, map[string]string{tmuxctl.OptProject: r.project,
		tmuxctl.OptBackend: "claude", tmuxctl.OptCwd: r.project, tmuxctl.OptGuarded: "1"}))
	row := r.waitRow(id, func(s server.SessionRow) bool { return s.Adopted })
	assert.True(t, row.GuardrailsStale)
}

// Stop on an owned session: Ctrl-C first, and the CLI that quits on it
// ends the session ("stopped"); one that ignores it is killed after the
// wait ("killed"); force kills at once. Each is forgotten.
func TestStopOwnedSessionInterruptsThenKills(t *testing.T) {
	r := newTmuxRig(t, "", true)
	polite := r.startSession()
	r.screenShows(polite.ID, "ready")
	code, res, body := r.stopSession(polite.ID, "")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "stopped", res.Outcome)
	assert.False(t, r.tmuxHas(polite.ID))

	r.stubbornCLI()
	r.ts.stopWait = 700 * time.Millisecond
	stubborn := r.startSession()
	r.screenShows(stubborn.ID, "ready")
	begin := time.Now()
	code, res, body = r.stopSession(stubborn.ID, `{}`)
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "killed", res.Outcome)
	assert.GreaterOrEqual(t, time.Since(begin), 700*time.Millisecond, "it waited for the interrupt first")
	assert.False(t, r.tmuxHas(stubborn.ID))

	forced := r.startSession()
	r.screenShows(forced.ID, "ready")
	r.ts.stopWait = time.Hour // force never waits
	code, res, body = r.stopSession(forced.ID, `{"force":true}`)
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "killed", res.Outcome)
	assert.False(t, r.tmuxHas(forced.ID))
	for _, id := range []string{polite.ID, stubborn.ID, forced.ID} {
		require.Eventually(t, func() bool { _, ok := r.orch.FindTmuxSession(id); return !ok }, 5*time.Second, 20*time.Millisecond)
	}
}

// A hand-started session in a controllable pane (sessions.tmux_control):
// Stop sends the interrupt keys only — a CLI that ignores them keeps its
// tmux session ("interrupted"), itervox never kills it, and force is
// refused (403 not_owned).
func TestStopHandStartedTmuxSessionSendsKeysOnly(t *testing.T) {
	r := newTmuxRig(t, "sessions:\n  tmux_control: true\n", true)
	stubborn := r.stubbornCLI()
	r.ts.stopWait = 500 * time.Millisecond
	r.handStarted(false)
	require.NoError(t, r.tmux.NewSession(context.Background(), "work", r.project, stubborn))
	r.waitRow(handSessionID, func(s server.SessionRow) bool { return s.Controllable })

	code, _, body := r.stopSession(handSessionID, `{"force":true}`)
	assert.Equal(t, http.StatusForbidden, code, body)
	assert.Contains(t, body, "not_owned")

	code, res, body := r.stopSession(handSessionID, "")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "interrupted", res.Outcome)
	assert.True(t, r.tmuxHas("work"), "itervox never kills a tmux session it did not start")
}

// A hand-started session outside tmux cannot be stopped from here (403
// not_controllable); its row carries no pane, and the panel shows the
// "stop it in its own terminal" hint instead.
func TestStopHandStartedSessionOutsideTmuxIsRefused(t *testing.T) {
	r := newTmuxRig(t, "sessions:\n  tmux_control: true\n", true)
	r.handStarted(false)
	row := r.waitRow(handSessionID, func(server.SessionRow) bool { return true })
	assert.Nil(t, row.TmuxPane)
	code, _, body := r.stopSession(handSessionID, "")
	assert.Equal(t, http.StatusForbidden, code, body)
	assert.Contains(t, body, "not_controllable")
	code, _, _ = r.stopSession("nope", "")
	assert.Equal(t, http.StatusNotFound, code)
}

// The feed records each stop (never pane content).
func TestStopIsRecordedInTheFeed(t *testing.T) {
	r := newTmuxRig(t, "", true)
	s := r.startSession()
	r.screenShows(s.ID, "ready")
	code, _, body := r.stopSession(s.ID, `{"force":true}`)
	require.Equal(t, http.StatusOK, code, body)
	require.Eventually(t, func() bool {
		for _, ev := range r.orch.Snapshot().Feed {
			if ev.Kind == orchestrator.FeedSessionStopped && ev.Identifier == s.ID {
				return ev.Payload.Outcome == "killed" && ev.Payload.Reason == "force"
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
}
