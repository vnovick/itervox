package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/tracker"
)

// #178 step 3: a discovery pass never lists a session itervox owns — a
// running run's, one waiting for input, one resuming, one continued under
// itervox (by its original or its current ID) — even though its transcript
// sits in the same CLI directory.
func TestExternalSessionsExcludeSessionsItervoxOwns(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, AgentSessionID: "run-sess"}
	state.InputRequiredIssues["ENG-2"] = &InputRequiredEntry{Identifier: "ENG-2", SessionID: "waiting-sess"}
	state.PendingInputResumes["ENG-3"] = &PendingInputResumeEntry{Identifier: "ENG-3", SessionID: "resuming-sess"}
	state.ManualSessions["manual-sess"] = &ManualSession{ID: "manual-sess", ResumeID: "manual-new"}
	var found []extsessions.Session
	for _, id := range []string{"run-sess", "waiting-sess", "resuming-sess", "manual-sess", "manual-new", "hand-1"} {
		found = append(found, extsessions.Session{ID: id, Backend: "claude"})
	}
	applyExternalSessions(&state, found)
	require.Len(t, state.ExternalSessions, 1)
	assert.Equal(t, "hand-1", state.ExternalSessions[0].ID)
}

// Sessions started outside itervox sort after every session itervox owns,
// most recently written first.
func TestSessionsSortExternalSessionsLast(t *testing.T) {
	now := time.Now()
	state := NewState(dependencyAuditConfig())
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, AgentSessionID: "run", StartedAt: now}
	state.ManualSessions["m"] = &ManualSession{ID: "m", Status: SessionStatusIdle}
	state.ExternalSessions = []extsessions.Session{
		{ID: "old", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "new", UpdatedAt: now.Add(-time.Hour), Running: true, RunningReason: "busy"},
	}
	var order []string
	for _, s := range Sessions(state, false) {
		order = append(order, s.ID)
	}
	assert.Equal(t, []string{"run", "m", "new", "old"}, order)
}

// The continue decision is the event loop's: a session that left the
// discovered list, a draining daemon and the cap are refused there.
func TestContinueSessionDecidedOnTheEventLoop(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	state := NewState(dependencyAuditConfig())
	req := func(id string) *ContinueRequest {
		return &ContinueRequest{Session: extsessions.Session{ID: id}, Reply: make(chan error, 1)}
	}
	r := req("gone")
	o.applyContinueSession(t.Context(), &state, r)
	assert.ErrorIs(t, <-r.Reply, ErrSessionNotFound)

	state.ExternalSessions = []extsessions.Session{{ID: "s1", Backend: "claude"}}
	state.Draining = true
	r = req("s1")
	o.applyContinueSession(t.Context(), &state, r)
	assert.ErrorIs(t, <-r.Reply, ErrSessionsDraining)
	state.Draining = false

	for i := range maxManualSessions {
		state.ManualSessions[string(rune('a'+i))] = &ManualSession{}
	}
	r = req("s1")
	o.applyContinueSession(t.Context(), &state, r)
	assert.ErrorIs(t, <-r.Reply, ErrTooManyManual)
	assert.Nil(t, state.ManualSessions["s1"])
}

type instantRunner struct{}

func (instantRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	return agent.TurnResult{SessionID: *sessionID}, nil
}

// An accepted continue moves the session from ExternalSessions to
// ManualSessions in the same event, so it is never listed twice.
func TestContinueSessionMovesItToManualSessions(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), instantRunner{}, nil)
	state := NewState(dependencyAuditConfig())
	state.ExternalSessions = []extsessions.Session{{ID: "s1", Backend: "claude", Cwd: "/w"}, {ID: "s2", Backend: "codex"}}
	r := &ContinueRequest{Session: extsessions.Session{ID: "s1"}, Reply: make(chan error, 1)}
	o.applyContinueSession(t.Context(), &state, r)
	require.NoError(t, <-r.Reply)
	require.NotNil(t, state.ManualSessions["s1"])
	assert.Equal(t, SessionStatusRunning, state.ManualSessions["s1"].Status)
	require.Len(t, state.ExternalSessions, 1)
	assert.Equal(t, "s2", state.ExternalSessions[0].ID)
	o.workersWg.Wait(time.Second)
}

// A drain waits for a continued session's turn in flight, like a worker's.
func TestDrainWaitsForAContinuedSessionsTurn(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	state.Draining = true
	state.ManualSessions["m"] = &ManualSession{ID: "m", Status: SessionStatusRunning}
	assert.True(t, manualTurnRunning(state))
	state.ManualSessions["m"].Status = SessionStatusIdle
	assert.False(t, manualTurnRunning(state))
}
