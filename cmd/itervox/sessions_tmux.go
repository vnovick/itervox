package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tmuxctl"
)

// Sessions panel, tmux control and "New session" (#178). Everything here
// runs on HTTP handler goroutines, never on the event loop; ownership of
// itervox's tmux sessions is event-loop state reached through the
// orchestrator's Reserve/Finish/EndTmuxSession events.
//
// Writing into a session itervox did not start is opt-in
// (sessions.tmux_control) and goes only through internal/tmuxctl. Before
// every write the pane is re-resolved: it must still exist and still host
// the session's CLI process, else pane_gone. Every write is audited with
// an slog line that names the session, pane, kind and size — never the
// text.

// maxWorkspaceFolders caps the workspaces "New session" offers.
const maxWorkspaceFolders = 50

// errNotControllable: no tmux pane itervox may control (mapped per
// endpoint: not_owned for messages, not_controllable otherwise).
var errNotControllable = errors.New("cmd/itervox: session not controllable")

// tmuxSessions backs the tmux endpoints of the Sessions API.
type tmuxSessions struct {
	orch *orchestrator.Orchestrator
	// tmux is shared with the detector (same server / socket).
	detect *tmuxDetector
	// control is sessions.tmux_control (read at startup).
	control       bool
	projectRoot   string
	workspaceRoot string
	agentCommand  string
	// processes lists the host's processes; nil uses SystemProcesses.
	processes func() ([]extsessions.Process, error)
	// lookPath resolves an agent CLI; nil uses exec.LookPath.
	lookPath func(string) (string, error)
}

func (t *tmuxSessions) tmux() tmuxctl.Client { return t.detect.client }

func (t *tmuxSessions) unavailable() error {
	return &server.TmuxUnavailableError{Hint: tmuxInstallHint()}
}

func (t *tmuxSessions) listProcesses() ([]extsessions.Process, error) {
	if t.processes != nil {
		return t.processes()
	}
	return extsessions.SystemProcesses()
}

// status is the "tmux" object of GET /api/v1/sessions.
func (t *tmuxSessions) status() server.TmuxStatusRow {
	st := t.detect.current()
	return server.TmuxStatusRow{Available: st.Available, Version: st.Version, InstallHint: tmuxInstallHint(), Control: t.control}
}

// attachCommand is what a terminal runs to attach to itervox's session id.
func (t *tmuxSessions) attachCommand(id string) string {
	if s := t.tmux().Socket; s != "" {
		return "tmux -S " + shellQuote(s) + " attach -t " + id
	}
	return "tmux attach -t " + id
}

// decorate fills the tmux fields of the session rows.
func (t *tmuxSessions) decorate(rows []server.SessionRow, sessions []orchestrator.AgentSession) {
	available := t.detect.current().Available
	for i := range rows {
		s := sessions[i]
		if s.TmuxPane.ID != "" {
			rows[i].TmuxPane = &server.TmuxPaneRow{ID: s.TmuxPane.ID, Label: s.TmuxPane.Label}
		}
		switch {
		case s.Kind == orchestrator.SessionKindTmux:
			rows[i].Controllable = available
			rows[i].AttachCommand = t.attachCommand(s.ID)
		case s.Owner == orchestrator.SessionOwnerExternal:
			rows[i].Controllable = available && t.control && s.TmuxPane.ID != ""
		}
	}
}

// target re-resolves the pane of the session id for a write or capture.
// handled is false when id is neither one of itervox's tmux sessions nor
// a listed hand-started session.
func (t *tmuxSessions) target(ctx context.Context, id string) (pane tmuxctl.Pane, handled bool, err error) {
	if own, ok := t.orch.FindTmuxSession(id); ok {
		if !t.detect.refresh().Available {
			return pane, true, t.unavailable()
		}
		panes, err := t.tmux().ListPanes(ctx)
		if err != nil {
			return pane, true, fmt.Errorf("cmd/itervox: list tmux panes: %w", err)
		}
		if pane, ok = sessionPane(panes, own.ID); !ok {
			return pane, true, server.ErrPaneGone
		}
		return pane, true, nil
	}
	ext, ok := t.orch.TranscriptSession(id)
	if !ok {
		return pane, false, nil
	}
	if !t.control {
		return pane, true, errNotControllable
	}
	if !t.detect.refresh().Available {
		return pane, true, t.unavailable()
	}
	if ext.TmuxPane.ID == "" || ext.PID <= 0 {
		return pane, true, errNotControllable
	}
	panes, err := t.tmux().ListPanes(ctx)
	if err != nil {
		return pane, true, fmt.Errorf("cmd/itervox: list tmux panes: %w", err)
	}
	found := false
	for _, p := range panes {
		if p.ID == ext.TmuxPane.ID {
			pane, found = p, true
		}
	}
	if !found {
		return pane, true, server.ErrPaneGone
	}
	procs, err := t.listProcesses()
	if err != nil {
		return pane, true, server.ErrPaneGone // cannot prove the pane still runs it
	}
	proc, alive := extsessions.FindProcess(procs, ext.PID)
	if !alive || extsessions.ProcessBackend(proc) != ext.Backend ||
		!tmuxctl.HostsProcess(pane, ext.PID, extsessions.Ancestors(procs, ext.PID)) {
		return pane, true, server.ErrPaneGone
	}
	return pane, true, nil
}

// audit records one write into a pane: never its content.
func audit(id string, pane tmuxctl.Pane, kind string, size int) {
	slog.Info("sessions: tmux send", "session_id", id, "pane", pane.ID, "kind", kind, "bytes", size)
	metrics.TmuxSend(kind)
}

// sendText pastes text into the session's pane (Enter when submit).
func (t *tmuxSessions) sendText(id, text string, submit bool) (server.SessionMessageRow, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*tmuxctl.DefaultTimeout)
	defer cancel()
	pane, handled, err := t.target(ctx, id)
	if !handled {
		return server.SessionMessageRow{}, false, nil
	}
	if errors.Is(err, errNotControllable) {
		return server.SessionMessageRow{}, true, server.ErrSessionNotOwned
	}
	if err != nil {
		return server.SessionMessageRow{}, true, err
	}
	if err := t.tmux().SendText(ctx, pane.ID, text, submit); err != nil {
		return server.SessionMessageRow{}, true, tmuxWriteError(err)
	}
	audit(id, pane, "text", len(text))
	now := time.Now().UTC()
	return server.SessionMessageRow{ID: "tmux-" + shortID(), Text: text, Status: "delivered", Delivery: "tmux",
		QueuedAt: now, DeliveredAt: &now}, true, nil
}

// tmuxWriteError maps a tmux failure: a pane that vanished between the
// check and the write is pane_gone.
func tmuxWriteError(err error) error {
	if errors.Is(err, tmuxctl.ErrNoSuchPane) {
		return server.ErrPaneGone
	}
	return fmt.Errorf("cmd/itervox: tmux: %w", err)
}

// screen captures the session's pane.
func (t *tmuxSessions) screen(id string, lines int) (server.ScreenResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*tmuxctl.DefaultTimeout)
	defer cancel()
	pane, handled, err := t.target(ctx, id)
	switch {
	case !handled:
		return server.ScreenResponse{}, server.ErrSessionNotFound
	case errors.Is(err, errNotControllable):
		return server.ScreenResponse{}, server.ErrNotControllable
	case err != nil:
		return server.ScreenResponse{}, err
	}
	if lines <= 0 {
		lines = tmuxctl.DefaultCaptureLines
	}
	text, err := t.tmux().Capture(ctx, pane.ID, lines)
	if err != nil {
		return server.ScreenResponse{}, tmuxWriteError(err)
	}
	return server.ScreenResponse{ID: id, Pane: pane.ID, Label: pane.Label(), Text: text, Lines: lines,
		CapturedAt: time.Now().UTC()}, nil
}

// sendKey presses one allowlisted key in the session's pane.
func (t *tmuxSessions) sendKey(id, key string) error {
	if !tmuxctl.AllowedKey(key) {
		return server.ErrKeyNotAllowed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*tmuxctl.DefaultTimeout)
	defer cancel()
	pane, handled, err := t.target(ctx, id)
	switch {
	case !handled:
		return server.ErrSessionNotFound
	case errors.Is(err, errNotControllable):
		return server.ErrNotControllable
	case err != nil:
		return err
	}
	if err := t.tmux().SendKey(ctx, pane.ID, key); err != nil {
		return tmuxWriteError(err)
	}
	audit(id, pane, "key", len(key))
	return nil
}

// start runs an interactive agent CLI in a new tmux session.
func (t *tmuxSessions) start(req server.NewSessionRequest) (orchestrator.AgentSession, error) {
	if !t.detect.refresh().Available {
		return orchestrator.AgentSession{}, t.unavailable()
	}
	cwd, err := resolveSessionCwd(req.Cwd, t.projectRoot, t.workspaceRoot)
	if err != nil {
		return orchestrator.AgentSession{}, err
	}
	configured, mode := t.agentCommand, agent.PermissionSandbox
	if req.Profile != "" {
		p, ok := t.orch.AgentProfileCfg(req.Profile)
		if !ok {
			return orchestrator.AgentSession{}, server.ErrUnknownProfile
		}
		if p.Command != "" {
			configured = p.Command
		}
		if p.PermissionMode != "" {
			mode = agent.ParsePermissionMode(p.PermissionMode)
		}
	}
	lookPath := t.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	command, err := interactiveCommand(req.Backend, configured, mode, req.Prompt, lookPath)
	if err != nil {
		return orchestrator.AgentSession{}, &server.BadSessionRequestError{Field: "backend", Message: err.Error()}
	}
	s := orchestrator.TmuxSession{ID: "itervox-" + shortID(), Backend: req.Backend, Cwd: cwd, Profile: req.Profile}
	switch err := t.orch.ReserveTmuxSession(s); {
	case errors.Is(err, orchestrator.ErrTooManyTmuxSessions):
		return orchestrator.AgentSession{}, server.ErrTooManySessions
	case errors.Is(err, orchestrator.ErrBusy):
		return orchestrator.AgentSession{}, server.ErrBusy
	case err != nil:
		return orchestrator.AgentSession{}, fmt.Errorf("cmd/itervox: reserve tmux session: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*tmuxctl.DefaultTimeout)
	defer cancel()
	err = t.tmux().NewSession(ctx, s.ID, cwd, command)
	t.orch.FinishTmuxSessionStart(s.ID, err != nil)
	if err != nil {
		return orchestrator.AgentSession{}, fmt.Errorf("cmd/itervox: tmux new-session: %w", err)
	}
	slog.Info("sessions: tmux session started", "session_id", s.ID, "backend", s.Backend, "cwd", cwd,
		"profile", s.Profile, "prompt_bytes", len(req.Prompt))
	return orchestrator.AgentSession{ID: s.ID, Owner: orchestrator.SessionOwnerItervox, Kind: orchestrator.SessionKindTmux,
		Backend: s.Backend, Cwd: cwd, Profile: s.Profile, Status: orchestrator.SessionStatusRunning, StartedAt: time.Now()}, nil
}

// kill kills one of itervox's tmux sessions.
func (t *tmuxSessions) kill(id string) error {
	if _, ok := t.orch.FindTmuxSession(id); !ok {
		if _, ext := t.orch.TranscriptSession(id); ext {
			return server.ErrSessionNotOwned
		}
		return server.ErrSessionNotFound
	}
	if !t.detect.refresh().Available {
		return t.unavailable()
	}
	ctx, cancel := context.WithTimeout(context.Background(), tmuxctl.DefaultTimeout)
	defer cancel()
	if err := t.tmux().KillSession(ctx, id); err != nil && !errors.Is(err, tmuxctl.ErrNoSuchPane) {
		return fmt.Errorf("cmd/itervox: tmux kill-session: %w", err)
	}
	t.orch.EndTmuxSession(id)
	slog.Info("sessions: tmux session killed", "session_id", id)
	return nil
}

// options lists what "New session" offers: the project root, then the
// itervox workspaces (directories under workspace.root), and the profiles.
func (t *tmuxSessions) options() server.NewSessionOptions {
	opts := server.NewSessionOptions{ProjectRoot: t.projectRoot, Folders: []string{t.projectRoot}, Max: orchestrator.MaxTmuxSessions}
	if t.workspaceRoot != "" {
		if entries, err := os.ReadDir(t.workspaceRoot); err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && len(opts.Folders) <= maxWorkspaceFolders {
					opts.Folders = append(opts.Folders, filepath.Join(t.workspaceRoot, e.Name()))
				}
			}
		}
	}
	for name := range t.orch.ProfilesCfg() {
		opts.Profiles = append(opts.Profiles, name)
	}
	sort.Strings(opts.Profiles)
	return opts
}

// resolveSessionCwd checks a "New session" folder: the project root (the
// default), a folder inside it, or an itervox workspace.
func resolveSessionCwd(cwd, projectRoot, workspaceRoot string) (string, error) {
	bad := func(msg string) error { return &server.BadSessionRequestError{Field: "cwd", Message: msg} }
	if cwd == "" {
		cwd = projectRoot
	}
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(projectRoot, cwd)
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", bad("folder does not exist")
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return "", bad("not a folder")
	}
	for _, root := range []string{projectRoot, workspaceRoot} {
		if root == "" {
			continue
		}
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		if isWithin(real, root) {
			return real, nil
		}
	}
	return "", bad("the folder must be the project folder, a folder inside it, or an itervox workspace")
}

// interactiveCommand builds the shell command tmux runs for a new session:
// the INTERACTIVE CLI (no -p, no stream-json), the configured command when
// its binary is the backend (keeping its own flags), else the backend's
// binary; the permission flags; and the first prompt as the positional
// argument. Everything we add is single-quoted.
func interactiveCommand(backend, configured string, mode agent.PermissionMode, prompt string, lookPath func(string) (string, error)) (string, error) {
	_, cmd := config.ParseBackendHint(configured)
	cmd = strings.TrimSpace(cmd)
	first := config.FirstCommandToken(cmd)
	if first == "" || filepath.Base(first) != backend {
		cmd, first = backend, backend
	}
	if _, err := lookPath(first); err != nil {
		return "", fmt.Errorf("%s was not found on the daemon's PATH", first)
	}
	parts := []string{cmd}
	for _, f := range agent.InteractivePermissionFlags(backend, mode) {
		parts = append(parts, shellQuote(f))
	}
	if p := strings.TrimSpace(prompt); p != "" {
		parts = append(parts, shellQuote(p))
	}
	return strings.Join(parts, " "), nil
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shortID is 6 random hex characters.
func shortID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
