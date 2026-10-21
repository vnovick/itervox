package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/extsessions"
)

// #178 step 3: a discovery pass never lists a session itervox owns — a
// running run's, one waiting for input, one resuming — even though its
// transcript sits in the same CLI directory.
func TestExternalSessionsExcludeSessionsItervoxOwns(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, AgentSessionID: "run-sess"}
	state.InputRequiredIssues["ENG-2"] = &InputRequiredEntry{Identifier: "ENG-2", SessionID: "waiting-sess"}
	state.PendingInputResumes["ENG-3"] = &PendingInputResumeEntry{Identifier: "ENG-3", SessionID: "resuming-sess"}
	var found []extsessions.Session
	for _, id := range []string{"run-sess", "waiting-sess", "resuming-sess", "hand-1"} {
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
	state.ExternalSessions = []extsessions.Session{
		{ID: "old", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "new", UpdatedAt: now.Add(-time.Hour), Running: true, RunningReason: "busy"},
	}
	var order []string
	for _, s := range Sessions(state, false) {
		order = append(order, s.ID)
	}
	assert.Equal(t, []string{"run", "new", "old"}, order)
}

// #178 tmux: a reservation is decided on the loop and capped at
// MaxTmuxSessions; a failed start drops it; a started one is listed as an
// itervox "tmux" session.
func TestTmuxSessionsReservedStartedAndCapped(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	now := time.Now()
	reserve := func(id string) error {
		ev := &TmuxSessionEvent{Session: TmuxSession{ID: id, Backend: "claude", Cwd: "/work/app"}, Reply: make(chan error, 1)}
		applyTmuxSessionEvent(&state, EventTmuxSessionReserve, ev, now)
		return <-ev.Reply
	}
	require.NoError(t, reserve("itervox-a"))
	assert.Equal(t, SessionStatusStarting, state.TmuxSessions["itervox-a"].Status)
	applyTmuxSessionEvent(&state, EventTmuxSessionStarted, &TmuxSessionEvent{Session: TmuxSession{ID: "itervox-a"}}, now)
	assert.Equal(t, SessionStatusRunning, state.TmuxSessions["itervox-a"].Status)

	require.NoError(t, reserve("itervox-b"))
	applyTmuxSessionEvent(&state, EventTmuxSessionStarted, &TmuxSessionEvent{Session: TmuxSession{ID: "itervox-b"}, Failed: true}, now)
	assert.Nil(t, state.TmuxSessions["itervox-b"], "a failed start is forgotten")

	for i := 1; i < MaxTmuxSessions; i++ {
		require.NoError(t, reserve("itervox-"+string(rune('c'+i))))
	}
	assert.ErrorIs(t, reserve("itervox-z"), ErrTooManyTmuxSessions)

	rows := Sessions(state, false)
	var tmuxRows int
	for _, r := range rows {
		if r.Kind == SessionKindTmux {
			tmuxRows++
			assert.Equal(t, SessionOwnerItervox, r.Owner)
		}
	}
	assert.Equal(t, MaxTmuxSessions, tmuxRows)
	applyTmuxSessionEvent(&state, EventTmuxSessionEnded, &TmuxSessionEvent{Session: TmuxSession{ID: "itervox-a"}}, now)
	assert.Nil(t, state.TmuxSessions["itervox-a"])
}

// A discovery pass attaches each tmux session's pane, drops one whose tmux
// session is gone (only when the pane list is complete and the session
// started before it was taken), and never lists the CLI session running in
// an itervox tmux session as hand-started.
func TestDiscoveryPassAttachesPanesAndDropsEndedTmuxSessions(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	before := time.Now().Add(-time.Minute)
	state.TmuxSessions["itervox-a"] = &TmuxSession{ID: "itervox-a", Status: SessionStatusRunning, StartedAt: before}
	state.TmuxSessions["itervox-b"] = &TmuxSession{ID: "itervox-b", Status: SessionStatusRunning, StartedAt: before}
	state.TmuxSessions["itervox-new"] = &TmuxSession{ID: "itervox-new", Status: SessionStatusRunning, StartedAt: time.Now().Add(time.Minute)}
	pane := extsessions.TmuxPane{ID: "%3", Label: "itervox-a:0.0", Session: "itervox-a"}

	applyTmuxPanes(&state, ExternalScan{TmuxListed: false, ScannedAt: time.Now()})
	assert.Len(t, state.TmuxSessions, 3, "an incomplete pass drops nothing")

	scan := ExternalScan{TmuxListed: true, TmuxPanes: []extsessions.TmuxPane{pane}, ScannedAt: time.Now(),
		Sessions: []extsessions.Session{{ID: "cli-in-a", TmuxPane: pane}, {ID: "hand-1"}}}
	applyTmuxPanes(&state, scan)
	applyExternalSessions(&state, scan.Sessions)
	assert.Equal(t, pane, state.TmuxSessions["itervox-a"].Pane)
	assert.Nil(t, state.TmuxSessions["itervox-b"], "its tmux session is gone")
	assert.NotNil(t, state.TmuxSessions["itervox-new"], "started after the pane list was taken")
	require.Len(t, state.ExternalSessions, 1)
	assert.Equal(t, "hand-1", state.ExternalSessions[0].ID)
}
