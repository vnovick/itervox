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
