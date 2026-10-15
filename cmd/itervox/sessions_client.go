package main

import (
	"errors"
	"time"

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
			Steering: s.Steering,
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
