package statusui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vnovick/itervox/internal/server"
)

// TestPaletteLightVariantsDifferFromDark (CORE-092): every palette entry is
// an AdaptiveColor with non-empty, distinct Light and Dark values, so light
// terminals get their own contrast instead of the dark-theme neon.
func TestPaletteLightVariantsDifferFromDark(t *testing.T) {
	if len(palette) == 0 {
		t.Fatal("palette is empty")
	}
	for name, c := range palette {
		if c.Light == "" || c.Dark == "" {
			t.Errorf("%s: Light=%q Dark=%q; both must be set", name, c.Light, c.Dark)
		}
		if strings.EqualFold(c.Light, c.Dark) {
			t.Errorf("%s: Light and Dark are the same (%s)", name, c.Light)
		}
	}
}

func sizedModel(t *testing.T, snap server.StateSnapshot, w, h int) Model {
	t.Helper()
	m := New(newTestSnap(snap), nil, Config{MaxAgents: 5, DashboardURL: "http://127.0.0.1:8090/"}, func(string) bool { return true })
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

func pressKey(m Model, r rune) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return next.(Model)
}

// TestHelpToggleShowsFullHelp (CORE-092): the footer shows a trimmed short
// help (at most 7 bindings, ending in "? more"); `?` flips to the full help
// and a second `?` restores the short one. The frame keeps the terminal
// height in both modes.
func TestHelpToggleShowsFullHelp(t *testing.T) {
	short := defaultKeys().ShortHelp()
	if len(short) > 7 {
		t.Fatalf("ShortHelp has %d bindings; want <= 7", len(short))
	}
	const w, h = 160, 50
	m := sizedModel(t, server.StateSnapshot{}, w, h)
	footer := func(m Model) string {
		lines := strings.Split(strings.TrimSuffix(ansi.Strip(m.View()), "\n"), "\n")
		if len(lines) != h {
			t.Fatalf("frame is %d lines for a %d-line terminal", len(lines), h)
		}
		for i, l := range lines {
			if strings.HasPrefix(l, "╚") {
				return strings.Join(lines[i+1:], "\n")
			}
		}
		t.Fatal("no footer rule")
		return ""
	}
	const fullOnly = "more workers" // in FullHelp only
	if f := footer(m); strings.Contains(f, fullOnly) || !strings.Contains(f, "?") {
		t.Fatalf("short help should hide %q and offer ?:\n%s", fullOnly, f)
	}
	m = pressKey(m, '?')
	if !m.help.ShowAll {
		t.Fatal("? did not switch to the full help")
	}
	if f := footer(m); !strings.Contains(f, fullOnly) {
		t.Fatalf("full help should list %q:\n%s", fullOnly, f)
	}
	m = pressKey(m, '?')
	if m.help.ShowAll {
		t.Fatal("a second ? must restore the short help")
	}
	if f := footer(m); strings.Contains(f, fullOnly) {
		t.Fatalf("short help restored should hide %q:\n%s", fullOnly, f)
	}
}

// TestNarrowTerminalSinglePane (CORE-092): below narrowWidth columns the TUI
// shows one pane (the focused one) instead of a 46-column list squeezed
// next to a 20-column floor, and no rendered line is wider than the terminal.
func TestNarrowTerminalSinglePane(t *testing.T) {
	snap := server.StateSnapshot{
		Running:       []server.RunningRow{{Identifier: "ENG-1", State: "In Progress", Backend: "claude", LastEvent: strings.Repeat("a long agent message ", 10)}},
		InputRequired: []server.InputRequiredRow{{Identifier: "ENG-2", State: "input_required"}},
		RateLimits:    &server.RateLimitInfo{RequestsLimit: 1000, RequestsRemaining: 5},
	}
	for _, w := range []int{40, 60, 69} {
		m := sizedModel(t, snap, w, 30)
		m.killMsg = "⚙ a status message long enough to overflow a narrow terminal line"
		view := m.View()
		for i, l := range strings.Split(view, "\n") {
			if got := lipgloss.Width(l); got > w {
				t.Fatalf("width %d: line %d is %d columns:\n%q", w, i, got, ansi.Strip(l))
			}
		}
		plain := ansi.Strip(view)
		if strings.Contains(plain, "[ LOGS") {
			t.Fatalf("width %d: the right pane is drawn next to the list:\n%s", w, plain)
		}
		// Tab moves focus to the log pane, which then fills the width alone.
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		right := ansi.Strip(next.(Model).View())
		if !strings.Contains(right, "LOGS") {
			t.Fatalf("width %d: focusing the log pane should show it:\n%s", w, right)
		}
		for i, l := range strings.Split(next.(Model).View(), "\n") {
			if got := lipgloss.Width(l); got > w {
				t.Fatalf("width %d (log pane): line %d is %d columns", w, i, got)
			}
		}
	}
}
