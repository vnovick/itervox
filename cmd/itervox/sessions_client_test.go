package main

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
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
