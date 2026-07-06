package statusui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/vnovick/itervox/internal/server"
)

// TestModel_RendersConfigInvalidBanner verifies the TUI surfaces a stale
// WORKFLOW.md state (T-26 piece 4) so the operator knows the daemon is
// running on the previously-valid config.
func TestModel_RendersConfigInvalidBanner(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		ConfigInvalid: &server.ConfigInvalidStatus{
			Path:         "WORKFLOW.md",
			Error:        "invalid cron expression: bad token",
			RetryAttempt: 4,
			RetryAt:      "2026-04-28T15:00:00Z",
		},
	})
	m := newMinimalModel(snap)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(140, 30))
	tm.Send(tea.WindowSizeMsg{Width: 140, Height: 30})
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	view := final.View()

	if !strings.Contains(view, "CONFIG INVALID") {
		t.Errorf("expected 'CONFIG INVALID' badge in TUI view, got:\n%s", view)
	}
	if !strings.Contains(view, "invalid cron expression") {
		t.Errorf("expected the error message to be surfaced in TUI view, got:\n%s", view)
	}
	if !strings.Contains(view, "retry 4") {
		t.Errorf("expected retry-attempt label 'retry 4' in TUI view, got:\n%s", view)
	}
}

// TestModel_NoConfigInvalidBannerWhenNil ensures we don't render the banner
// during normal operation (snap.ConfigInvalid is nil).
func TestModel_NoConfigInvalidBannerWhenNil(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{})
	m := newMinimalModel(snap)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(140, 30))
	tm.Send(tea.WindowSizeMsg{Width: 140, Height: 30})
	_ = tm.Quit()

	final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model)
	view := final.View()

	if strings.Contains(view, "CONFIG INVALID") {
		t.Errorf("did not expect 'CONFIG INVALID' badge in healthy TUI view, got:\n%s", view)
	}
}
