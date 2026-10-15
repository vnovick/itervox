package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// sessionsClient backs the Sessions panel API (#178) over the orchestrator:
// rows come from Snapshot(), messages go through SendSessionMessage (an
// event to the loop). liveSteering is agent.live_steering, read-only after
// startup.
type sessionsClient struct {
	orch         *orchestrator.Orchestrator
	liveSteering bool
}

func (c sessionsClient) ListSessions() []server.SessionRow {
	return sessionRows(orchestrator.Sessions(c.orch.Snapshot(), c.liveSteering))
}

func (c sessionsClient) SendSessionMessage(id, text string) (server.SessionMessageRow, error) {
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
	case errors.Is(err, orchestrator.ErrSessionInUse):
		var inUse *orchestrator.SessionInUseError
		if errors.As(err, &inUse) {
			return &server.SessionInUseError{Reason: inUse.Reason}
		}
		return &server.SessionInUseError{Reason: "it may still be running"}
	case errors.Is(err, orchestrator.ErrSessionAlreadyOwned):
		return server.ErrSessionAlreadyOwned
	case errors.Is(err, orchestrator.ErrSessionsDraining):
		return server.ErrDraining
	case errors.Is(err, orchestrator.ErrTooManyManual):
		return server.ErrTooManySessions
	case errors.Is(err, orchestrator.ErrUnknownProfile):
		return server.ErrUnknownProfile
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
			InUseReason: s.InUseReason, LastError: s.LastError,
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

// SessionTranscript reads a page of a hand-started or continued session's
// transcript (#178 step 3). Only a session the orchestrator lists can be
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

// ContinueSession resumes a hand-started session under itervox.
func (c sessionsClient) ContinueSession(id, message, profile string) (server.SessionRow, error) {
	s, err := c.orch.ContinueExternalSession(id, message, profile)
	if err != nil {
		return server.SessionRow{}, sessionError(err)
	}
	return sessionRows([]orchestrator.AgentSession{s})[0], nil
}

// wireExternalSessions enables discovery of sessions started by hand on
// this host (sessions.external). Off in demo mode.
func wireExternalSessions(orch *orchestrator.Orchestrator, cfg *config.Config, workflowPath string, demo bool) {
	if demo || !cfg.Sessions.External {
		return
	}
	orch.SetExternalSessionSource(extsessions.Scanner{Options: externalSessionOptions(cfg, workflowPath)}, 0)
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
