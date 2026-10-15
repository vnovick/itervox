package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestSessionsClientMapsRowsAndErrors: orchestrator sessions become wire
// rows (zero times omitted, messages carried) and orchestrator refusals the
// server's sentinels.
func TestSessionsClientMapsRowsAndErrors(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	rows := sessionRows([]orchestrator.AgentSession{{
		ID: "sess-1", RunID: "run-1", Owner: orchestrator.SessionOwnerItervox, Identifier: "ENG-1",
		Status: orchestrator.SessionStatusRunning, StartedAt: at, Steering: orchestrator.SteeringResume,
		Messages: []orchestrator.SessionMessage{{ID: "m1", Text: "hi", Status: orchestrator.SessionMessageQueued, QueuedAt: at}},
	}})
	require.Len(t, rows, 1)
	assert.Equal(t, "sess-1", rows[0].ID)
	assert.Equal(t, "itervox", rows[0].Owner)
	assert.Equal(t, at, *rows[0].StartedAt)
	assert.Nil(t, rows[0].QueuedAt)
	assert.Equal(t, "queued", rows[0].Messages[0].Status)
	assert.Nil(t, rows[0].Messages[0].DeliveredAt)

	cfg := &config.Config{}
	c := sessionsClient{orch: orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), nil, nil)}
	_, err := c.SendSessionMessage("nope", "hi")
	assert.ErrorIs(t, err, server.ErrSessionNotFound)
	_, err = c.SendSessionMessage("nope", "")
	assert.ErrorIs(t, err, server.ErrInvalidSessionMessage)
	for in, want := range map[error]error{
		orchestrator.ErrSessionInputRequired: server.ErrSessionInputRequired,
		orchestrator.ErrSessionNotOwned:      server.ErrSessionNotOwned,
		orchestrator.ErrBusy:                 server.ErrBusy,
	} {
		assert.ErrorIs(t, sessionError(in), want)
	}
	other := errors.New("x")
	assert.Equal(t, other, sessionError(other))
	assert.Empty(t, c.ListSessions())
}

// #178 step 3: discovery is scoped to the project (WORKFLOW.md directory
// plus sessions.external_roots) and never lists itervox's own workspace
// root.
func TestExternalSessionOptionsScopeToTheProject(t *testing.T) {
	cfg := &config.Config{}
	cfg.Sessions = config.SessionsConfig{External: true, ExternalWindowHours: 6, ExternalScope: "project", ExternalRoots: []string{"/srv/shared", "web"}}
	cfg.Workspace.Root = "/home/u/.itervox/workspaces/app"
	o := externalSessionOptions(cfg, "/work/app/WORKFLOW.md")
	assert.Equal(t, []string{"/work/app", "/srv/shared", "/work/app/web"}, o.Roots)
	assert.Equal(t, []string{"/home/u/.itervox/workspaces/app"}, o.Exclude)
	assert.Equal(t, 6*time.Hour, o.Window)
	assert.Equal(t, "project", o.Scope)

	cfg.Workspace.Root = "/work" // contains the project: excluding it would hide everything
	assert.Empty(t, externalSessionOptions(cfg, "/work/app/WORKFLOW.md").Exclude)
}

// Continue refusals reach the server as its sentinels, the in-use reason
// included.
func TestSessionsClientMapsContinueErrors(t *testing.T) {
	var inUse *server.SessionInUseError
	err := sessionError(&orchestrator.SessionInUseError{Reason: "claude process 7 is running"})
	require.True(t, errors.As(err, &inUse))
	assert.Equal(t, "claude process 7 is running", inUse.Reason)
	for in, want := range map[error]error{
		orchestrator.ErrSessionAlreadyOwned: server.ErrSessionAlreadyOwned,
		orchestrator.ErrSessionsDraining:    server.ErrDraining,
		orchestrator.ErrTooManyManual:       server.ErrTooManySessions,
		orchestrator.ErrUnknownProfile:      server.ErrUnknownProfile,
	} {
		assert.ErrorIs(t, sessionError(in), want)
	}
	c := sessionsClient{orch: orchestrator.New(&config.Config{}, tracker.NewMemoryTracker(nil, nil, nil), nil, nil)}
	_, err = c.SessionTranscript("nope", -1, 10)
	assert.ErrorIs(t, err, server.ErrSessionNotFound, "only a listed session's transcript can be read")
	_, err = c.ContinueSession("nope", "", "")
	assert.ErrorIs(t, err, server.ErrSessionNotFound)
}

type recordingRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, prompt, workspacePath, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, *sessionID+"|"+workspacePath+"|"+prompt)
	return agent.TurnResult{SessionID: *sessionID}, nil
}

// End to end over the real scanner: a Claude session started by hand in
// the project is listed as external, its transcript is served (redacted),
// and continuing it resumes it in its directory and makes it owned.
func TestSessionsClientServesHandStartedSessions(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "app")
	require.NoError(t, os.MkdirAll(project, 0o755))
	id := "11111111-2222-4333-8444-555555555555"
	dir := filepath.Join(home, "projects", extsessions.EncodeClaudeProjectDir(project))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	transcript := `{"type":"user","cwd":"` + project + `","sessionId":"` + id + `","message":{"role":"user","content":"set DB_PASSWORD=hunter2hunter2"}}` + "\n" +
		`{"type":"assistant","cwd":"` + project + `","sessionId":"` + id + `","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n"
	path := filepath.Join(dir, id+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(transcript), 0o644))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))

	cfg := &config.Config{}
	cfg.Agent.Command = "claude"
	cfg.Polling.IntervalMs = 50
	r := &recordingRunner{}
	o := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), r, nil)
	o.SetExternalSessionSource(extsessions.Scanner{
		Options:   extsessions.Options{ClaudeProjectsDir: filepath.Join(home, "projects"), Window: 24 * time.Hour, Roots: []string{project}},
		Processes: func() ([]extsessions.Process, error) { return nil, nil },
	}, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = o.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	c := sessionsClient{orch: o}

	require.Eventually(t, func() bool { return len(c.ListSessions()) == 1 }, 2*time.Second, 10*time.Millisecond)
	row := c.ListSessions()[0]
	assert.Equal(t, server.SessionRow{ID: id, Owner: "external", Backend: "claude", Status: "exited", Cwd: project, UpdatedAt: row.UpdatedAt}, row)

	page, err := c.SessionTranscript(id, -1, 0)
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	assert.Equal(t, "set DB_PASSWORD=***", page.Entries[0].Text)
	assert.Equal(t, "done", page.Entries[1].Text)

	got, err := c.ContinueSession(id, "next step", "")
	require.NoError(t, err)
	assert.Equal(t, "itervox", got.Owner)
	require.Eventually(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.calls) == 1
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, id+"|"+project+"|next step", r.calls[0])
	require.Eventually(t, func() bool {
		rows := c.ListSessions()
		return len(rows) == 1 && rows[0].Owner == "itervox" && rows[0].Status == "idle"
	}, 2*time.Second, 10*time.Millisecond)
	_, err = c.SessionTranscript(id, -1, 0)
	assert.NoError(t, err, "a continued session's transcript stays readable")
}
