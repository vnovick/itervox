package statusui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/vnovick/itervox/internal/server"
)

// CORE-055 — the TUI header names a limited agent backend (and never shows
// the segment while every backend is healthy, so healthy renders and their
// golden files are unchanged).

func renderSnapView(t *testing.T, s server.StateSnapshot) string {
	t.Helper()
	m := newMinimalModel(newTestSnap(s))
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(160, 30))
	tm.Send(tea.WindowSizeMsg{Width: 160, Height: 30})
	_ = tm.Quit()
	return tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(Model).View()
}

func TestModel_BackendHealthHeaderSegment(t *testing.T) {
	reset := time.Date(2026, 9, 27, 15, 4, 0, 0, time.Local)
	retry := time.Date(2026, 9, 27, 12, 15, 0, 0, time.Local)
	view := renderSnapView(t, server.StateSnapshot{
		BackendHealth: []server.BackendHealthRow{
			{Backend: "claude", Status: server.BackendHealthLimited, LimitedUntil: &reset, RetryAt: &reset, HeldIssues: 2, ReroutedIssues: 1},
			{Backend: "codex", Status: server.BackendHealthHealthy},
			{Backend: "codex", Host: "build-1", Status: server.BackendHealthLimited, RetryAt: &retry},
		},
		AutoSwitches: []server.AutoSwitchRow{{Identifier: "ENG-3", Source: "backend_fallback"}},
	})
	// BH-M3-9: the clock shows the date when the reset is not today.
	now := time.Now()
	for _, want := range []string{"LIMITS", "claude limited until " + resetClock(reset, now), "2 held", "1 rerouted",
		"codex@build-1 limited (reset unknown, retry " + resetClock(retry, now) + ")", "1 auto-switched"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected %q in the TUI header, got:\n%s", want, view)
		}
	}
	if strings.Contains(view, "codex healthy") {
		t.Errorf("healthy backends are not listed in the segment:\n%s", view)
	}

	healthy := renderSnapView(t, server.StateSnapshot{
		BackendHealth: []server.BackendHealthRow{{Backend: "claude", Status: server.BackendHealthHealthy}},
	})
	if strings.Contains(healthy, "LIMITS") {
		t.Errorf("no LIMITS segment while every backend is healthy:\n%s", healthy)
	}
}

// BH-M3-9: a reset that is not today shows its date (a seven_day reset must
// not read as "today"), and the row never exceeds the terminal width.
func TestBackendHealthHeaderLine_DateAndWidth(t *testing.T) {
	now := time.Now()
	later := now.Add(3 * 24 * time.Hour)
	s := server.StateSnapshot{BackendHealth: []server.BackendHealthRow{
		{Backend: "claude", Status: server.BackendHealthLimited, LimitedUntil: &later, RetryAt: &later},
		{Backend: "codex", Host: "a-very-long-build-host-name.example.internal", Status: server.BackendHealthLimited, RetryAt: &later, HeldIssues: 12, ReroutedIssues: 3},
	}}
	line := xansi.Strip(backendHealthHeaderLine(s, now, 400))
	if want := "claude limited until " + later.Local().Format("Jan 2 15:04"); !strings.Contains(line, want) {
		t.Errorf("want %q in %q", want, line)
	}
	soon := now.Add(time.Minute)
	if soon.YearDay() == now.YearDay() {
		s2 := server.StateSnapshot{BackendHealth: []server.BackendHealthRow{{Backend: "claude", Status: server.BackendHealthLimited, LimitedUntil: &soon}}}
		if l := xansi.Strip(backendHealthHeaderLine(s2, now, 400)); !strings.Contains(l, "until "+soon.Local().Format("15:04")) || strings.Contains(l, soon.Local().Format("Jan 2")) {
			t.Errorf("a reset later today shows HH:MM only: %q", l)
		}
	}
	for _, w := range []int{40, 80} {
		if got := xansi.StringWidth(backendHealthHeaderLine(s, now, w)); got > w {
			t.Errorf("width %d: row is %d cells wide", w, got)
		}
	}
}
