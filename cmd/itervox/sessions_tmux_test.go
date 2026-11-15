package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tmuxctl"
	"github.com/vnovick/itervox/internal/tracker"
)

// #178 tmux control and "New session", end to end: a real orchestrator, the
// real Sessions API, and real tmux on a private socket (never the default
// server), with fake `claude` / `codex` scripts that record their arguments
// and directory and echo every line they read.

const handSessionID = "11111111-2222-4333-8444-666666666666"

// pausableSource repeats its last pass while paused, so a test can change
// tmux under a session without discovery noticing first. A pass holds mu
// throughout, so pause() also waits out a pass already in flight.
type pausableSource struct {
	inner  sessionSource
	mu     sync.Mutex
	paused bool
	last   orchestrator.ExternalScan
}

func (p *pausableSource) Discover(now time.Time) (orchestrator.ExternalScan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused {
		return p.last, nil
	}
	scan, err := p.inner.Discover(now)
	p.last = scan
	return scan, err
}

func (p *pausableSource) pause() {
	p.mu.Lock()
	p.paused = true
	p.mu.Unlock()
}

type tmuxRig struct {
	src     *pausableSource
	t       *testing.T
	project string
	home    string
	bin     string
	tmux    tmuxctl.Client
	orch    *orchestrator.Orchestrator
	ts      *tmuxSessions
	srv     *server.Server
}

// fakeCLI writes a fake agent CLI named name into bin: it records its
// arguments and directory under out, then echoes what it reads.
func fakeCLI(t *testing.T, bin, out, name string) string {
	t.Helper()
	path := filepath.Join(bin, name)
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(filepath.Join(out, name+".args")) + "\n" +
		"pwd > " + shellQuote(filepath.Join(out, name+".cwd")) + "\n" +
		"echo ready\nwhile IFS= read -r line; do echo \"got: $line\"; done\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
}

func newTmuxRig(t *testing.T, sessionsYAML string, needTmux bool) *tmuxRig {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil && needTmux {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	r := &tmuxRig{t: t, project: filepath.Join(root, "app"), home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin")}
	for _, d := range []string{r.project, r.home, r.bin, filepath.Join(root, "out"), filepath.Join(root, "ws", "ENG-1")} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	var err error
	r.project, err = filepath.EvalSymlinks(r.project)
	require.NoError(t, err)
	claude := fakeCLI(t, r.bin, filepath.Join(root, "out"), "claude")
	fakeCLI(t, r.bin, filepath.Join(root, "out"), "codex")
	t.Setenv("PATH", r.bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	workflow := filepath.Join(r.project, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(workflow, []byte("---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: k\n  project_slug: p\n"+
		"workspace:\n  root: "+filepath.Join(root, "ws")+"\n"+sessionsYAML+"---\n\nPrompt.\n"), 0o644))
	cfg, err := config.Load(workflow)
	require.NoError(t, err)

	sockDir, err := os.MkdirTemp("", "itx")
	require.NoError(t, err)
	r.tmux = tmuxctl.Client{Socket: filepath.Join(sockDir, "s")}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", r.tmux.Socket, "kill-server").Run()
		_ = os.RemoveAll(sockDir)
	})

	// Only this rig's agent processes count: the host may run real ones.
	procs := func() ([]extsessions.Process, error) {
		all, err := extsessions.SystemProcesses()
		var out []extsessions.Process
		for _, p := range all {
			if extsessions.ProcessBackend(p) != "" && !strings.HasPrefix(p.Cwd, root) {
				continue
			}
			out = append(out, p)
		}
		return out, err
	}
	cfg.Polling.IntervalMs = 50
	r.orch = orchestrator.New(cfg, tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	detect := &tmuxDetector{client: r.tmux}
	r.ts = &tmuxSessions{orch: r.orch, detect: detect, control: cfg.Sessions.TmuxControl, projectRoot: r.project,
		workspaceRoot: cfg.Workspace.Root, agentCommand: claude, processes: procs}
	r.src = &pausableSource{inner: sessionSource{
		scanner: &extsessions.Scanner{Options: extsessions.Options{ClaudeProjectsDir: filepath.Join(r.home, "projects"),
			Window: 24 * time.Hour, Roots: []string{r.project}}, Processes: procs},
		detect: detect,
	}}
	r.orch.SetExternalSessionSource(r.src, 30*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.orch.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	r.srv = server.New(server.Config{
		Snapshot:     func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		Sessions:     sessionsClient{orch: r.orch, tmux: r.ts},
		AllowedHosts: []string{"example.com"},
	})
	return r
}

func (r *tmuxRig) do(method, path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.srv.ServeHTTP(w, req)
	return w
}

func (r *tmuxRig) list() server.SessionsResponse {
	r.t.Helper()
	var resp server.SessionsResponse
	w := r.do(http.MethodGet, "/api/v1/sessions", "")
	require.Equal(r.t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(r.t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

func (r *tmuxRig) row(id string) (server.SessionRow, bool) {
	for _, s := range r.list().Sessions {
		if s.ID == id {
			return s, true
		}
	}
	return server.SessionRow{}, false
}

// handStarted writes the transcript of a Claude session started by hand in
// the project and, when inTmux, runs the fake claude for it in tmux
// session "work" (as an operator would).
func (r *tmuxRig) handStarted(inTmux bool) {
	r.t.Helper()
	dir := filepath.Join(r.home, "projects", extsessions.EncodeClaudeProjectDir(r.project))
	require.NoError(r.t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, handSessionID+".jsonl")
	line := `{"type":"user","cwd":"` + r.project + `","sessionId":"` + handSessionID + `","message":{"role":"user","content":"hi"}}` + "\n"
	require.NoError(r.t, os.WriteFile(path, []byte(line), 0o644))
	old := time.Now().Add(-time.Hour)
	require.NoError(r.t, os.Chtimes(path, old, old))
	if inTmux {
		require.NoError(r.t, r.tmux.NewSession(context.Background(), "work", r.project, filepath.Join(r.bin, "claude")))
	}
}

func (r *tmuxRig) waitRow(id string, ok func(server.SessionRow) bool) server.SessionRow {
	r.t.Helper()
	var got server.SessionRow
	require.Eventually(r.t, func() bool {
		row, found := r.row(id)
		got = row
		return found && ok(row)
	}, 5*time.Second, 20*time.Millisecond, "session %s never reached the wanted state; last %+v", id, got)
	return got
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Field   string `json:"field"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	return body.Error.Code
}

func (r *tmuxRig) screenShows(id, want string) {
	r.t.Helper()
	var last string
	require.Eventually(r.t, func() bool {
		w := r.do(http.MethodGet, "/api/v1/sessions/"+id+"/screen?lines=50", "")
		if w.Code != http.StatusOK {
			last = w.Body.String()
			return false
		}
		var s server.ScreenResponse
		_ = json.Unmarshal(w.Body.Bytes(), &s)
		last = s.Text
		return strings.Contains(s.Text, want)
	}, 5*time.Second, 30*time.Millisecond, "screen never showed %q; last: %s", want, last)
}

// sessions.tmux_control defaults to off: a hand-started session running in
// a matched tmux pane is listed with its pane but is not controllable, and
// messages (403 not_owned), keys and screen (403 not_controllable) are
// refused.
func TestTmuxControlIsOffByDefault(t *testing.T) {
	r := newTmuxRig(t, "", true)
	r.handStarted(true)
	row := r.waitRow(handSessionID, func(s server.SessionRow) bool { return s.TmuxPane != nil })
	assert.False(t, row.Controllable)
	assert.False(t, r.list().Tmux.Control)
	assert.True(t, r.list().Tmux.Available)

	w := r.do(http.MethodPost, "/api/v1/sessions/"+handSessionID+"/message", `{"text":"hello"}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "not_owned", errorCode(t, w))
	w = r.do(http.MethodPost, "/api/v1/sessions/"+handSessionID+"/keys", `{"key":"Enter"}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "not_controllable", errorCode(t, w))
	w = r.do(http.MethodGet, "/api/v1/sessions/"+handSessionID+"/screen", "")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "not_controllable", errorCode(t, w))
}

// A discovered session gets the pane its CLI process runs in: the process
// (found by liveness) is the pane's own or its descendant, and the pane's
// directory is the session's. Real tmux and /proc; then the rules on
// synthetic data: another directory, an unrelated process, and two
// sessions of one directory pointing at one process (the newest wins).
func TestPaneMatchedToSessionByProcessAncestry(t *testing.T) {
	r := newTmuxRig(t, "sessions:\n  tmux_control: true\n", true)
	r.handStarted(true)
	row := r.waitRow(handSessionID, func(s server.SessionRow) bool { return s.TmuxPane != nil })
	assert.Equal(t, "work:0.0", row.TmuxPane.Label)
	assert.True(t, tmuxctl.ValidPaneID(row.TmuxPane.ID))
	assert.True(t, row.Controllable)

	dir, other := t.TempDir(), t.TempDir()
	now := time.Now()
	procs := []extsessions.Process{{PID: 10, PPID: 1}, {PID: 11, PPID: 10}, {PID: 12, PPID: 11}, {PID: 20, PPID: 1}, {PID: 21, PPID: 20}}
	panes := []tmuxctl.Pane{{ID: "%1", PID: 10, Cwd: dir, Session: "a"}, {ID: "%2", PID: 20, Cwd: other, Session: "b"}}
	sessions := []extsessions.Session{
		{ID: "newer", Cwd: dir, PID: 12, UpdatedAt: now},
		{ID: "older", Cwd: dir, PID: 12, UpdatedAt: now.Add(-time.Hour)},
		{ID: "elsewhere", Cwd: dir, PID: 21, UpdatedAt: now},
		{ID: "no-process", Cwd: dir, UpdatedAt: now},
	}
	matchSessionPanes(sessions, procs, panes)
	assert.Equal(t, extsessions.TmuxPane{ID: "%1", Label: "a:0.0", Session: "a"}, sessions[0].TmuxPane)
	assert.Empty(t, sessions[1].TmuxPane.ID, "an older session of the same process does not take the pane")
	// The other order: a newer session listed later takes the pane over.
	swapped := []extsessions.Session{sessions[1], sessions[0]}
	swapped[0].TmuxPane, swapped[1].TmuxPane = extsessions.TmuxPane{}, extsessions.TmuxPane{}
	matchSessionPanes(swapped, procs, panes)
	assert.Empty(t, swapped[0].TmuxPane.ID)
	assert.Equal(t, "%1", swapped[1].TmuxPane.ID)
	assert.Empty(t, sessions[2].TmuxPane.ID, "a pane in another directory is not matched")
	assert.Empty(t, sessions[3].TmuxPane.ID)
}

// With sessions.tmux_control on, a message to a hand-started session in a
// matched pane is pasted through tmux and the program receives it; once the
// pane is gone the next message is refused with 409 pane_gone.
func TestMessageToControllableExternalSessionGoesThroughTmux(t *testing.T) {
	r := newTmuxRig(t, "sessions:\n  tmux_control: true\n", true)
	r.handStarted(true)
	r.waitRow(handSessionID, func(s server.SessionRow) bool { return s.Controllable })
	r.screenShows(handSessionID, "ready")

	w := r.do(http.MethodPost, "/api/v1/sessions/"+handSessionID+"/message", `{"text":"run the tests"}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"delivery":"tmux"`)
	r.screenShows(handSessionID, "got: run the tests")

	w = r.do(http.MethodPost, "/api/v1/sessions/"+handSessionID+"/message", `{"text":"half","submit":false}`)
	require.Equal(t, http.StatusAccepted, w.Code)
	w = r.do(http.MethodPost, "/api/v1/sessions/"+handSessionID+"/keys", `{"key":"Enter"}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	r.screenShows(handSessionID, "got: half")

	// The pane goes away between two discovery passes: the send-time check
	// catches it.
	r.src.pause()
	require.NoError(t, r.tmux.KillSession(context.Background(), "work"))
	w = r.do(http.MethodPost, "/api/v1/sessions/"+handSessionID+"/message", `{"text":"anyone?"}`)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "pane_gone", errorCode(t, w))
	w = r.do(http.MethodPost, "/api/v1/sessions/"+handSessionID+"/keys", `{"key":"Enter"}`)
	assert.Equal(t, "pane_gone", errorCode(t, w))
}

// Every write into a pane is audited — session, pane, kind, size — and the
// log line never carries the text; the writes are counted.
func TestTmuxSendAuditLogNeverContainsText(t *testing.T) {
	r := newTmuxRig(t, "sessions:\n  tmux_control: true\n", true)
	r.handStarted(true)
	row := r.waitRow(handSessionID, func(s server.SessionRow) bool { return s.Controllable })

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	secret := "deploy with token SECRET-TEXT-12345"
	w := r.do(http.MethodPost, "/api/v1/sessions/"+handSessionID+"/message", `{"text":"`+secret+`"}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	w = r.do(http.MethodPost, "/api/v1/sessions/"+handSessionID+"/keys", `{"key":"C-c"}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	slog.SetDefault(prev)

	logs := buf.String()
	assert.NotContains(t, logs, "SECRET-TEXT-12345")
	assert.NotContains(t, logs, "deploy with token")
	var sends []string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, `msg="sessions: tmux send"`) {
			sends = append(sends, l)
		}
	}
	require.Len(t, sends, 2, logs)
	for _, want := range []string{"session_id=" + handSessionID, "pane=" + row.TmuxPane.ID, "kind=text", "bytes=35"} {
		assert.Contains(t, sends[0], want)
	}
	assert.Contains(t, sends[1], "kind=key")
	assert.Contains(t, sends[1], "bytes=3")

	var exp bytes.Buffer
	require.NoError(t, metrics.WriteText(&exp, metrics.View{}))
	assert.Contains(t, exp.String(), `itervox_tmux_sends_total{kind="text"}`)
	assert.Contains(t, exp.String(), `itervox_tmux_sends_total{kind="key"}`)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	var raw []byte
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(path)
		raw = b
		return err == nil && len(b) > 0
	}, 5*time.Second, 20*time.Millisecond, "%s never written", path)
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// "New session" starts the INTERACTIVE CLI (no -p, no stream-json) in a new
// tmux session itervox-<id> in the chosen folder, with the confining
// permission mode and the first prompt as its positional argument; it is
// listed as itervox's, controllable, with the attach command.
func TestNewSessionStartsInteractiveCLIInTmux(t *testing.T) {
	r := newTmuxRig(t, "", true)
	out := filepath.Join(filepath.Dir(r.bin), "out")

	w := r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"claude","prompt":"fix the 'login' bug"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		Session server.SessionRow `json:"session"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.Session.ID
	assert.Regexp(t, `^itervox-[0-9a-f]{6}$`, id)
	assert.Equal(t, "tmux", created.Session.Kind)
	assert.Equal(t, "itervox", created.Session.Owner)
	assert.True(t, created.Session.Controllable)
	assert.Equal(t, "tmux -S "+shellQuote(r.tmux.Socket)+" attach -t "+id, created.Session.AttachCommand)

	args := readLines(t, filepath.Join(out, "claude.args"))
	assert.NotContains(t, args, "-p")
	assert.NotContains(t, args, "--print")
	assert.NotContains(t, strings.Join(args, " "), "stream-json")
	assert.Equal(t, []string{"--permission-mode", "acceptEdits", "fix the 'login' bug"}, args)
	assert.Equal(t, []string{r.project}, readLines(t, filepath.Join(out, "claude.cwd")))

	panes, err := r.tmux.ListPanes(context.Background())
	require.NoError(t, err)
	_, found := sessionPane(panes, id)
	assert.True(t, found, "tmux session %s exists", id)
	row := r.waitRow(id, func(s server.SessionRow) bool { return s.TmuxPane != nil })
	assert.Equal(t, id+":0.0", row.TmuxPane.Label)
	assert.Equal(t, r.project, row.Cwd)
	r.screenShows(id, "ready")
	w = r.do(http.MethodPost, "/api/v1/sessions/"+id+"/message", `{"text":"go on"}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	r.screenShows(id, "got: go on")

	// Codex, in an itervox workspace: its own binary and sandbox.
	ws := filepath.Join(r.ts.workspaceRoot, "ENG-1")
	w = r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"codex","cwd":"`+ws+`"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, []string{"--sandbox", "workspace-write"}, readLines(t, filepath.Join(out, "codex.args")))
	realWS, _ := filepath.EvalSymlinks(ws)
	assert.Equal(t, []string{realWS}, readLines(t, filepath.Join(out, "codex.cwd")))
	opts := r.list().NewSession
	assert.Equal(t, []string{r.project, ws}, opts.Folders)
}

// The folder must be the project root (default), inside it, or an itervox
// workspace: anything else is 400 with field "cwd".
func TestNewSessionRejectsCwdOutsideProject(t *testing.T) {
	r := newTmuxRig(t, "", true)
	require.NoError(t, os.MkdirAll(filepath.Join(r.project, "web"), 0o755))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(r.project, "escape")))
	for _, cwd := range []string{outside, "/", "..", filepath.Join(r.project, "escape"), filepath.Join(r.project, "missing")} {
		w := r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"claude","cwd":"`+cwd+`"}`)
		assert.Equal(t, http.StatusBadRequest, w.Code, cwd)
		assert.Contains(t, w.Body.String(), `"field":"cwd"`, cwd)
	}
	got, err := resolveSessionCwd("web", r.project, r.ts.workspaceRoot)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(r.project, "web"), got)
	got, err = resolveSessionCwd("", r.project, r.ts.workspaceRoot)
	require.NoError(t, err)
	assert.Equal(t, r.project, got)
	w := r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"bash"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"field":"backend"`)
	w = r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"claude","profile":"nope"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"field":"profile"`)
}

// Without tmux on PATH, "New session" is refused with 409 tmux_unavailable
// and an install hint, and GET /api/v1/sessions says tmux is unavailable.
func TestNewSessionRequiresTmux(t *testing.T) {
	r := newTmuxRig(t, "sessions:\n  tmux_control: true\n", false)
	t.Setenv("PATH", r.bin) // the fake CLIs, but no tmux
	w := r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"claude"}`)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "tmux_unavailable", errorCode(t, w))
	assert.Contains(t, w.Body.String(), tmuxInstallHint())
	st := r.list().Tmux
	assert.False(t, st.Available)
	assert.Equal(t, tmuxInstallHint(), st.InstallHint)
	assert.True(t, st.Control)
}

// Without tmux on PATH every write is refused with 409 tmux_unavailable,
// even with sessions.tmux_control on: messages and keys to a hand-started
// session and to an itervox tmux session, the screen, and kill.
func TestTmuxUnavailableRefusesEveryWrite(t *testing.T) {
	r := newTmuxRig(t, "sessions:\n  tmux_control: true\n", false)
	t.Setenv("PATH", r.bin)
	r.handStarted(false)
	r.waitRow(handSessionID, func(s server.SessionRow) bool { return !s.Controllable && s.TmuxPane == nil })
	require.NoError(t, r.orch.ReserveTmuxSession(orchestrator.TmuxSession{ID: "itervox-abcdef", Backend: "claude", Cwd: r.project}))
	r.orch.FinishTmuxSessionStart("itervox-abcdef", false)
	// tmux is re-detected on every discovery pass, and a pass that began
	// before PATH lost tmux may still report it: wait for one that did not.
	own := r.waitRow("itervox-abcdef", func(s server.SessionRow) bool { return s.Status == "running" && !s.Controllable })
	assert.False(t, own.Controllable, "not controllable while tmux is missing")

	for _, id := range []string{handSessionID, "itervox-abcdef"} {
		for _, c := range []struct{ method, path, body string }{
			{http.MethodPost, "/message", `{"text":"hi"}`},
			{http.MethodPost, "/keys", `{"key":"Enter"}`},
			{http.MethodGet, "/screen", ""},
		} {
			w := r.do(c.method, "/api/v1/sessions/"+id+c.path, c.body)
			assert.Equal(t, http.StatusConflict, w.Code, id+c.path+": "+w.Body.String())
			assert.Equal(t, "tmux_unavailable", errorCode(t, w), id+c.path)
		}
	}
	w := r.do(http.MethodDelete, "/api/v1/sessions/itervox-abcdef", "")
	assert.Equal(t, "tmux_unavailable", errorCode(t, w))
}

// DELETE kills only a tmux session itervox started: 403 for a hand-started
// session (even one running in tmux), 404 for an unknown id.
func TestKillSessionOnlyForItervoxStarted(t *testing.T) {
	r := newTmuxRig(t, "sessions:\n  tmux_control: true\n", true)
	r.handStarted(true)
	r.waitRow(handSessionID, func(s server.SessionRow) bool { return s.TmuxPane != nil })

	w := r.do(http.MethodDelete, "/api/v1/sessions/"+handSessionID, "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Equal(t, "not_owned", errorCode(t, w))
	w = r.do(http.MethodDelete, "/api/v1/sessions/nope", "")
	assert.Equal(t, http.StatusNotFound, w.Code)
	panes, err := r.tmux.ListPanes(context.Background())
	require.NoError(t, err)
	_, alive := sessionPane(panes, "work")
	assert.True(t, alive, "the hand-started tmux session is untouched")

	w = r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"claude"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		Session server.SessionRow `json:"session"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	w = r.do(http.MethodDelete, "/api/v1/sessions/"+created.Session.ID, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	panes, err = r.tmux.ListPanes(context.Background())
	require.NoError(t, err)
	_, alive = sessionPane(panes, created.Session.ID)
	assert.False(t, alive)
	require.Eventually(t, func() bool { _, ok := r.row(created.Session.ID); return !ok }, 5*time.Second, 20*time.Millisecond)
}
