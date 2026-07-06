// Whitebox teatest tests for the statusui Model.
// These tests drive the full bubbletea Update→View pipeline using the
// charmbracelet/x/exp/teatest harness.
package statusui

import (
	"bytes"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/server"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestSnap returns a snap function that always returns the given snapshot.
func newTestSnap(s server.StateSnapshot) func() server.StateSnapshot {
	return func() server.StateSnapshot { return s }
}

// newMinimalModel builds a Model suitable for teatest with a minimal stub snap.
func newMinimalModel(snap func() server.StateSnapshot) Model {
	if snap == nil {
		snap = newTestSnap(server.StateSnapshot{})
	}
	cancelFn := func(id string) bool { return true }
	return New(snap, logbuffer.New(), Config{MaxAgents: 5}, cancelFn)
}

// ---------------------------------------------------------------------------
// WindowSizeMsg — sets width/height and initialises viewport (ready=true)
// ---------------------------------------------------------------------------

func TestModel_WindowSizeSetsReady(t *testing.T) {
	m := newMinimalModel(nil)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	assert.Equal(t, 80, final.width)
	assert.Equal(t, 24, final.height)
	assert.True(t, final.ready, "model should be ready after WindowSizeMsg")
}

func TestModel_WindowSizeUpdatesAfterResize(t *testing.T) {
	m := newMinimalModel(nil)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	// Send a different terminal size to confirm width/height are updated.
	tm.Send(tea.WindowSizeMsg{Width: 120, Height: 40})
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	assert.Equal(t, 120, final.width)
	assert.Equal(t, 40, final.height)
	assert.True(t, final.ready, "model stays ready after resize")
}

// ---------------------------------------------------------------------------
// tickMsg — syncs sessions/history/paused/retrying from snapshot
// ---------------------------------------------------------------------------

func TestModel_TickUpdatesSessions(t *testing.T) {
	snap := server.StateSnapshot{
		Running: []server.RunningRow{
			{Identifier: "PROJ-1", State: "Running"},
			{Identifier: "PROJ-2", State: "Running"},
		},
	}
	m := newMinimalModel(newTestSnap(snap))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tickMsg(time.Now()))
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	require.Len(t, final.sessions, 2, "tick must sync sessions from snapshot")
	assert.Equal(t, "PROJ-1", final.sessions[0].Identifier)
	assert.Equal(t, "PROJ-2", final.sessions[1].Identifier)
}

func TestModel_TickUpdatesHistory(t *testing.T) {
	snap := server.StateSnapshot{
		History: []server.HistoryRow{
			{Identifier: "PROJ-10", Status: "succeeded"},
		},
	}
	m := newMinimalModel(newTestSnap(snap))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tickMsg(time.Now()))
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	require.Len(t, final.history, 1)
	assert.Equal(t, "PROJ-10", final.history[0].Identifier)
}

func TestModel_TickUpdatesPaused(t *testing.T) {
	snap := server.StateSnapshot{
		Paused: []string{"PROJ-3", "PROJ-4"},
	}
	m := newMinimalModel(newTestSnap(snap))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tickMsg(time.Now()))
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	assert.Equal(t, []string{"PROJ-3", "PROJ-4"}, final.paused)
}

func TestModel_TickUpdatesInputRows(t *testing.T) {
	snap := server.StateSnapshot{
		InputRequired: []server.InputRequiredRow{
			{Identifier: "PROJ-5", State: "input_required"},
			{Identifier: "PROJ-6", State: "pending_input_resume"},
		},
	}
	m := newMinimalModel(newTestSnap(snap))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tickMsg(time.Now()))
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	require.Len(t, final.inputRows, 2, "tick must sync input-related rows from snapshot")
	assert.Equal(t, "PROJ-5", final.inputRows[0].Identifier)
	assert.Equal(t, "pending_input_resume", final.inputRows[1].State)
}

func TestModel_TickSyncsMaxAgentsFromSnapshot(t *testing.T) {
	snap := server.StateSnapshot{
		MaxConcurrentAgents: 12,
	}
	m := newMinimalModel(newTestSnap(snap))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tickMsg(time.Now()))
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	assert.Equal(t, 12, final.cfg.MaxAgents, "MaxAgents must be updated from snapshot.MaxConcurrentAgents")
}

// ---------------------------------------------------------------------------
// backlogLoadedMsg — async backlog data arrives
// ---------------------------------------------------------------------------

func TestModel_BacklogLoadedMsg_SetsItems(t *testing.T) {
	m := newMinimalModel(nil)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(backlogLoadedMsg{
		items: []BacklogIssueItem{
			{Identifier: "PROJ-5", Title: "Fix bug", State: "Backlog"},
		},
	})
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	assert.True(t, final.backlogOpen)
	require.Len(t, final.backlogItems, 1)
	assert.Equal(t, "PROJ-5", final.backlogItems[0].Identifier)
	assert.Equal(t, 0, final.backlogCursor)
}

func TestModel_BacklogLoadedMsg_ErrorClosesPanel(t *testing.T) {
	m := newMinimalModel(nil)
	m.backlogOpen = true // pretend it was open
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(backlogLoadedMsg{err: assert.AnError})
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	assert.False(t, final.backlogOpen, "error must close backlog panel")
	assert.NotEmpty(t, final.backlogErr)
}

// ---------------------------------------------------------------------------
// pickerLoadedMsg — project picker data arrives
// ---------------------------------------------------------------------------

func TestModel_PickerLoadedMsg_SetsProjects(t *testing.T) {
	m := newMinimalModel(nil)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(pickerLoadedMsg{
		projects: []ProjectItem{
			{ID: "p1", Name: "Alpha", Slug: "alpha"},
			{ID: "p2", Name: "Beta", Slug: "beta"},
		},
	})
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	assert.True(t, final.pickerOpen)
	assert.Len(t, final.pickerProjects, 2)
	assert.Empty(t, final.pickerErr)
}

func TestModel_PickerLoadedMsg_ErrorClosesPanel(t *testing.T) {
	m := newMinimalModel(nil)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(pickerLoadedMsg{err: assert.AnError})
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	assert.False(t, final.pickerOpen)
	assert.NotEmpty(t, final.pickerErr)
}

// ---------------------------------------------------------------------------
// Quit key — 'q' must exit the program
// ---------------------------------------------------------------------------

func TestModel_QuitKey(t *testing.T) {
	m := newMinimalModel(nil)
	// Give it a real terminal size so View() doesn't return an empty placeholder.
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	tm.Type("q")
	// WaitFinished verifies the program exited cleanly.
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

// ---------------------------------------------------------------------------
// View — rendered output after initialisation
// ---------------------------------------------------------------------------

func TestModel_ViewContainsAgentsHeader(t *testing.T) {
	// The TUI header renders "AGENTS" and "ISSUES" — verify the layout is present.
	m := newMinimalModel(nil)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.WindowSizeMsg{Width: 80, Height: 24})

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("AGENTS")) && bytes.Contains(bts, []byte("ISSUES"))
	}, teatest.WithDuration(3*time.Second))

	_ = tm.Quit()
}

func TestModel_ViewShowsIssueIdentifierAfterTick(t *testing.T) {
	snap := server.StateSnapshot{
		Running: []server.RunningRow{
			{Identifier: "PROJ-99", State: "Running"},
		},
	}
	m := newMinimalModel(newTestSnap(snap))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	tm.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
	tm.Send(tickMsg(time.Now()))

	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte("PROJ-99"))
	}, teatest.WithDuration(3*time.Second))

	_ = tm.Quit()
}
