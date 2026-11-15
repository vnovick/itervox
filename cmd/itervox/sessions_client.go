package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// sessionsClient backs the Sessions panel API (#178) over the orchestrator:
// rows come from Snapshot(), messages go through SendSessionMessage (an
// event to the loop). liveSteering is agent.live_steering, read-only after
// startup. tmux backs control of sessions in tmux panes and "New session"
// (sessions_tmux.go).
type sessionsClient struct {
	orch         *orchestrator.Orchestrator
	liveSteering bool
	tmux         *tmuxSessions
}

func (c sessionsClient) ListSessions() []server.SessionRow {
	sessions := orchestrator.Sessions(c.orch.Snapshot(), c.liveSteering)
	rows := sessionRows(sessions)
	if c.tmux != nil {
		c.tmux.decorate(rows, sessions)
	}
	return rows
}

// SendSessionMessage pastes into a controllable tmux session's pane, or
// hands the message to the orchestrator (itervox's issue runs; a
// hand-started session it refuses as not owned).
func (c sessionsClient) SendSessionMessage(id, text string, submit bool) (server.SessionMessageRow, error) {
	if c.tmux != nil {
		if row, handled, err := c.tmux.sendText(id, text, submit); handled {
			return row, err
		}
	}
	msg, err := c.orch.SendSessionMessage(id, text)
	if err != nil {
		return server.SessionMessageRow{}, sessionError(err)
	}
	return sessionMessageRow(msg), nil
}

// sessionError translates orchestrator errors into the server's sentinels
// (internal/server cannot import internal/orchestrator).
func sessionError(err error) error {
	switch {
	case errors.Is(err, orchestrator.ErrSessionNotFound):
		return server.ErrSessionNotFound
	case errors.Is(err, orchestrator.ErrSessionInputRequired):
		return server.ErrSessionInputRequired
	case errors.Is(err, orchestrator.ErrSessionNotOwned):
		return server.ErrSessionNotOwned
	case errors.Is(err, orchestrator.ErrInvalidSessionMessage):
		return server.ErrInvalidSessionMessage
	case errors.Is(err, orchestrator.ErrBusy):
		return server.ErrBusy
	default:
		return err
	}
}

func sessionRows(sessions []orchestrator.AgentSession) []server.SessionRow {
	rows := make([]server.SessionRow, 0, len(sessions))
	for _, s := range sessions {
		row := server.SessionRow{
			ID: s.ID, RunID: s.RunID, Owner: s.Owner, Identifier: s.Identifier, Title: s.Title,
			Profile: s.Profile, Backend: s.Backend, WorkerHost: s.WorkerHost, Kind: s.Kind,
			Status: s.Status, StartedAt: timePtr(s.StartedAt), QueuedAt: timePtr(s.QueuedAt),
			TurnCount: s.TurnCount, LastEvent: s.LastEvent, InputContext: s.InputContext,
			Steering: s.Steering, Cwd: s.Cwd, UpdatedAt: timePtr(s.UpdatedAt),
			InUseReason: s.InUseReason,
		}
		for _, m := range s.Messages {
			row.Messages = append(row.Messages, sessionMessageRow(m))
		}
		rows = append(rows, row)
	}
	return rows
}

func sessionMessageRow(m orchestrator.SessionMessage) server.SessionMessageRow {
	return server.SessionMessageRow{
		ID: m.ID, Text: m.Text, Status: string(m.Status), Delivery: m.Delivery,
		Reason: m.Reason, QueuedAt: m.QueuedAt.UTC(), DeliveredAt: timePtr(m.DeliveredAt),
	}
}

// timePtr is nil for the zero time, so omitempty leaves it out.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// SessionTranscript reads a page of a hand-started session's transcript (#178 step 3). Only a session the orchestrator lists can be
// read: the path comes from discovery, never from the client.
func (c sessionsClient) SessionTranscript(id string, before int64, limit int) (server.TranscriptResponse, error) {
	s, ok := c.orch.TranscriptSession(id)
	if !ok {
		return server.TranscriptResponse{}, server.ErrSessionNotFound
	}
	page, err := extsessions.ReadTranscript(s, before, limit)
	if err != nil {
		return server.TranscriptResponse{}, fmt.Errorf("cmd/itervox: read transcript: %w", err)
	}
	resp := server.TranscriptResponse{ID: s.ID, Backend: s.Backend, HasMore: page.HasMore, Size: page.Size,
		Entries: make([]server.TranscriptEntryRow, 0, len(page.Entries))}
	if page.HasMore {
		b := page.Before
		resp.Before = &b
	}
	for _, e := range page.Entries {
		resp.Entries = append(resp.Entries, server.TranscriptEntryRow{
			Role: e.Role, Kind: e.Kind, Text: e.Text, At: timePtr(e.At), Truncated: e.Truncated,
		})
	}
	return resp, nil
}

// TmuxStatus reports whether tmux is installed and tmux_control is on.
func (c sessionsClient) TmuxStatus() server.TmuxStatusRow { return c.tmux.status() }

// NewSessionOptions lists what "New session" offers.
func (c sessionsClient) NewSessionOptions() server.NewSessionOptions { return c.tmux.options() }

// SessionScreen captures a controllable session's pane.
func (c sessionsClient) SessionScreen(id string, lines int) (server.ScreenResponse, error) {
	return c.tmux.screen(id, lines)
}

// SendSessionKey presses an allowlisted key in a controllable session.
func (c sessionsClient) SendSessionKey(id, key string) error { return c.tmux.sendKey(id, key) }

// StartSession starts an interactive agent CLI in a new tmux session.
func (c sessionsClient) StartSession(req server.NewSessionRequest) (server.SessionRow, error) {
	s, err := c.tmux.start(req)
	if err != nil {
		return server.SessionRow{}, err
	}
	sessions := []orchestrator.AgentSession{s}
	rows := sessionRows(sessions)
	c.tmux.decorate(rows, sessions)
	return rows[0], nil
}

// KillSession kills a tmux session itervox started.
func (c sessionsClient) KillSession(id string) error { return c.tmux.kill(id) }

// tmuxCarry holds itervox's tmux sessions across a WORKFLOW.md reload.
var tmuxCarry struct {
	mu   sync.Mutex
	list []orchestrator.TmuxSession
}

// carryRunState hands a finished generation's carried state to the next:
// the RecentFailures ring and its acks (CORE-046), and itervox's tmux
// sessions (#178).
func carryRunState(final orchestrator.State) {
	failureCarry.store(final.RecentFailures, final.FailureAcks)
	tmuxCarry.mu.Lock()
	tmuxCarry.list = orchestrator.TmuxSessionList(final)
	tmuxCarry.mu.Unlock()
}

// wireSessions sets up the Sessions panel's discovery and tmux control for
// one run generation: discovery of sessions started by hand on this host
// (sessions.external) and of tmux panes runs off the event loop, and the
// tmux sessions the previous generation owned are carried over. Demo mode
// discovers nothing.
func wireSessions(orch *orchestrator.Orchestrator, cfg *config.Config, workflowPath string, demo bool) *tmuxSessions {
	tmuxCarry.mu.Lock()
	orch.SeedTmuxSessions(tmuxCarry.list)
	tmuxCarry.list = nil
	tmuxCarry.mu.Unlock()
	project := filepath.Dir(workflowPath)
	if abs, err := filepath.Abs(project); err == nil {
		project = abs
	}
	t := &tmuxSessions{orch: orch, detect: &tmuxDetector{}, control: cfg.Sessions.TmuxControl,
		projectRoot: project, workspaceRoot: cfg.Workspace.Root, agentCommand: cfg.Agent.Command,
		isolation: cfg.Agent.Isolation}
	if demo {
		return t
	}
	src := sessionSource{detect: t.detect}
	if cfg.Sessions.External {
		src.scanner = &extsessions.Scanner{Options: externalSessionOptions(cfg, workflowPath)}
	}
	orch.SetExternalSessionSource(src, 0)
	return t
}

// externalSessionOptions scopes discovery: by default to the WORKFLOW.md
// directory plus sessions.external_roots (relative ones resolved against
// it), and never the itervox workspace root — itervox's own worktrees —
// unless that root contains the project itself.
func externalSessionOptions(cfg *config.Config, workflowPath string) extsessions.Options {
	project := filepath.Dir(workflowPath)
	if abs, err := filepath.Abs(project); err == nil {
		project = abs
	}
	roots := []string{project}
	for _, r := range cfg.Sessions.ExternalRoots {
		if !filepath.IsAbs(r) {
			r = filepath.Join(project, r)
		}
		roots = append(roots, r)
	}
	var exclude []string
	if ws := cfg.Workspace.Root; ws != "" && !isWithin(project, ws) {
		exclude = append(exclude, ws)
	}
	return extsessions.Options{
		ClaudeProjectsDir: extsessions.DefaultClaudeProjectsDir(),
		CodexSessionsDir:  extsessions.DefaultCodexSessionsDir(),
		Window:            time.Duration(cfg.Sessions.ExternalWindowHours) * time.Hour,
		Scope:             cfg.Sessions.ExternalScope,
		Roots:             roots,
		Exclude:           exclude,
	}
}

// isWithin reports whether dir is root or inside it (a workspace root that
// contains the project must not hide the project's own sessions).
func isWithin(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
