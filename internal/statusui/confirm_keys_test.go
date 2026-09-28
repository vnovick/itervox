package statusui

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/server"
)

// CORE-020 — D discards PAUSED rows only, behind a y/N confirm; stopping a
// RUNNING row is the separate, confirmed S key whose prompt names the tracker
// state the orchestrator will actually move the issue to.

func runningAndPausedModel(t *testing.T) (Model, *[]string) {
	t.Helper()
	var terminated []string
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "R-1"}},
		Paused:  []string{"P-1"},
	})
	m := readyModel(snap)
	m.cfg.TerminateIssue = func(id string) bool {
		terminated = append(terminated, id)
		return true
	}
	return m, &terminated
}

func TestDiscardKeyIgnoresRunningRow(t *testing.T) {
	m, terminated := runningAndPausedModel(t)
	require.False(t, m.inPausedSection, "cursor starts on the running row")

	m = updateKey(m, "D")
	assert.Empty(t, *terminated, "D must never terminate a running row")
	assert.Empty(t, m.confirmKind, "D on a running row must not arm a confirm")
	assert.Contains(t, m.killMsg, "S")

	m = updateKey(m, "y")
	assert.Empty(t, *terminated, "a stray y after D on a running row must not terminate")
}

func TestDiscardKeyConfirmCancelDoesNotTerminate(t *testing.T) {
	m, terminated := runningAndPausedModel(t)
	m.inPausedSection = true
	m.pausedCursor = 0

	m = updateKey(m, "D")
	assert.Equal(t, "discard", m.confirmKind)
	assert.Contains(t, m.killMsg, "Discard P-1?")
	assert.Contains(t, m.killMsg, "y/N")
	assert.Empty(t, *terminated, "D alone must not discard")

	m = updateKey(m, "n")
	assert.Empty(t, *terminated, "any key other than y cancels")
	assert.Empty(t, m.confirmKind)
	assert.Contains(t, m.killMsg, "nothing discarded")

	// esc also cancels.
	m = updateKey(m, "D")
	m = updateKeyType(m, tea.KeyEsc)
	assert.Empty(t, *terminated)
	assert.Empty(t, m.confirmKind)
}

func TestDiscardKeyConfirmAcceptTerminatesPaused(t *testing.T) {
	m, terminated := runningAndPausedModel(t)
	m.inPausedSection = true
	m.pausedCursor = 0

	m = updateKey(m, "D")
	assert.Empty(t, *terminated, "D alone must not discard")
	m = updateKey(m, "y")
	assert.Equal(t, []string{"P-1"}, *terminated)
	assert.Contains(t, m.killMsg, "Discarded P-1")
	assert.Empty(t, m.confirmKind)
}

func TestStopKeyConfirmAcceptTerminatesRunning(t *testing.T) {
	tests := []struct {
		name          string
		backlogStates []string
		activeStates  []string
		wantTarget    string
		wantWording   string
	}{
		{
			name:          "backlog configured",
			backlogStates: []string{"Backlog", "Icebox"},
			activeStates:  []string{"Todo", "In Progress"},
			wantTarget:    `"Backlog"`,
			wantWording:   "re-dispatch possible if the move fails",
		},
		{
			name:         "no backlog falls back to first active state",
			activeStates: []string{"Todo", "In Progress"},
			wantTarget:   `"Todo"`,
			wantWording:  "an active state, so it will be re-dispatched",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, terminated := runningAndPausedModel(t)
			m.cfg.BacklogStates = tc.backlogStates
			m.cfg.TodoStates = tc.activeStates

			m = updateKey(m, "S")
			assert.Equal(t, "stop", m.confirmKind)
			assert.Contains(t, m.killMsg, "Stop R-1?")
			assert.Contains(t, m.killMsg, "running turn is killed")
			assert.Contains(t, m.killMsg, tc.wantTarget)
			assert.Contains(t, m.killMsg, tc.wantWording)
			assert.Contains(t, m.killMsg, "y/N")
			assert.Empty(t, *terminated, "S alone must not stop")

			m = updateKey(m, "y")
			assert.Equal(t, []string{"R-1"}, *terminated)
			assert.Contains(t, m.killMsg, "Stopped R-1")
			assert.Empty(t, m.confirmKind)
		})
	}
}

func TestStopKeyNoCallbackIsNoop(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "R-1"}},
	})
	m := readyModel(snap)
	m.cfg.TerminateIssue = nil

	m = updateKey(m, "S")
	assert.Empty(t, m.confirmKind, "no confirm without a TerminateIssue callback")
	m = updateKey(m, "y")
	assert.Empty(t, m.confirmKind)

	m = updateKey(m, "D")
	assert.Empty(t, m.confirmKind, "D is also inert without a callback")
}

func TestStopAndDiscardHelpLabels(t *testing.T) {
	keys := defaultKeys()
	assert.Equal(t, "discard paused", keys.Terminate.Help().Desc)
	assert.Equal(t, "D", keys.Terminate.Help().Key)
	assert.Equal(t, "S", keys.Stop.Help().Key)
	assert.Contains(t, keys.Stop.Help().Desc, "stop")
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")}, keys.Stop))
	assert.False(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}, keys.Stop),
		"lowercase s stays the split toggle")
}
