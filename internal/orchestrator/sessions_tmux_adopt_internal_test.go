package orchestrator

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/extsessions"
)

func adoptPane(id string) extsessions.TmuxPane {
	return extsessions.TmuxPane{ID: "%" + fmt.Sprint(len(id)), Label: id + ":0.0", Session: id}
}

// A discovery pass re-adopts the sessions its source lists as itervox's
// own for this project (after a daemon restart): only from a complete
// listing, only with a live pane, marked adopted with their stale
// guardrails, and the CLI session inside one is no longer listed as
// hand-started (read-only).
func TestDiscoveryPassReadoptsOwnTmuxSessions(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	created := time.Now().Add(-time.Hour)
	own := TmuxSession{ID: "itervox-e4e47c", Backend: "claude", Cwd: "/p/apptest", StartedAt: created, GuardrailsStale: true}
	gone := TmuxSession{ID: "itervox-000000", Backend: "codex"}
	pane := adoptPane(own.ID)

	applyDiscoveryScan(&state, ExternalScan{TmuxListed: false, TmuxOwned: []TmuxSession{own}, ScannedAt: time.Now()})
	assert.Empty(t, state.TmuxSessions, "an incomplete listing adopts nothing")

	scan := ExternalScan{TmuxListed: true, ScannedAt: time.Now(), TmuxPanes: []extsessions.TmuxPane{pane},
		TmuxOwned: []TmuxSession{own, gone},
		Sessions:  []extsessions.Session{{ID: "cli-in-own", Running: true, TmuxPane: pane}}}
	assert.True(t, applyDiscoveryScan(&state, scan))
	got := state.TmuxSessions[own.ID]
	require.NotNil(t, got)
	assert.True(t, got.Adopted)
	assert.True(t, got.GuardrailsStale)
	assert.Equal(t, SessionStatusRunning, got.Status)
	assert.Equal(t, pane, got.Pane)
	assert.Equal(t, created, got.StartedAt)
	assert.Nil(t, state.TmuxSessions[gone.ID], "no pane, no adoption")
	assert.Empty(t, state.ExternalSessions, "the CLI in an adopted session is itervox's, not hand-started")

	rows := Sessions(state, false)
	require.Len(t, rows, 1)
	assert.Equal(t, SessionOwnerItervox, rows[0].Owner)
	assert.Equal(t, SessionKindTmux, rows[0].Kind)
	assert.True(t, rows[0].Adopted)
	assert.True(t, rows[0].GuardrailsStale)

	assert.False(t, applyDiscoveryScan(&state, scan), "the same pass again changes nothing")
}

// Re-adoption respects MaxTmuxSessions: the sessions over the cap stay
// unowned.
func TestReadoptionRespectsTmuxSessionCap(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	scan := ExternalScan{TmuxListed: true, ScannedAt: time.Now()}
	for i := 0; i < MaxTmuxSessions+3; i++ {
		id := fmt.Sprintf("itervox-%06x", i)
		scan.TmuxOwned = append(scan.TmuxOwned, TmuxSession{ID: id, Backend: "claude"})
		scan.TmuxPanes = append(scan.TmuxPanes, extsessions.TmuxPane{ID: fmt.Sprintf("%%%d", i), Session: id})
	}
	applyDiscoveryScan(&state, scan)
	assert.Len(t, state.TmuxSessions, MaxTmuxSessions)
}

// A session itervox stopped after a pass was taken is not re-adopted by
// that (stale) pass; a pass taken after the stop that still lists it (it
// came back under the same tagged name) may adopt it again.
func TestStoppedTmuxSessionIsNotReadoptedByAStalePass(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	own := TmuxSession{ID: "itervox-abcdef", Backend: "claude"}
	scannedAt := time.Now()
	state.TmuxSessions[own.ID] = &TmuxSession{ID: own.ID, Status: SessionStatusRunning, StartedAt: scannedAt.Add(-time.Hour)}
	applyTmuxSessionEvent(&state, EventTmuxSessionEnded, &TmuxSessionEvent{Session: TmuxSession{ID: own.ID}}, scannedAt.Add(time.Second))

	stale := ExternalScan{TmuxListed: true, ScannedAt: scannedAt, TmuxOwned: []TmuxSession{own},
		TmuxPanes: []extsessions.TmuxPane{adoptPane(own.ID)}}
	applyDiscoveryScan(&state, stale)
	assert.Nil(t, state.TmuxSessions[own.ID], "stopped after the pass was taken")

	later := stale
	later.ScannedAt = scannedAt.Add(2 * time.Second)
	applyDiscoveryScan(&state, later)
	assert.NotNil(t, state.TmuxSessions[own.ID])

	far := later
	far.ScannedAt = scannedAt.Add(tmuxEndedMemory + time.Minute)
	applyDiscoveryScan(&state, far)
	assert.NotContains(t, state.TmuxSessionsEnded, own.ID, "the memory of a stop is bounded")
}
