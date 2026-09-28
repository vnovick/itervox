package statusui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/server"
)

// forceTrueColor switches the default lipgloss renderer to a TrueColor profile
// for the duration of the test and restores the previous profile afterwards.
// The package test binary normally runs with the Ascii profile (no TTY), which
// emits no escape sequences at all — that is exactly why the catwalk goldens
// never caught the byte-slice clip (CORE-072).
//
// The untyped constant 0 is termenv.TrueColor (the first iota of
// termenv.Profile); passing it untyped avoids promoting termenv to a direct
// module dependency just for this test.
func forceTrueColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(0)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
	if !strings.Contains(styleGreen.Render("x"), "\x1b[") {
		t.Fatal("forceTrueColor: styles still render without ANSI; the test would prove nothing")
	}
}

// unterminatedCSI returns the byte offset of the first CSI sequence (ESC '[')
// that is not closed by a final byte in 0x40..0x7E, or of a bare ESC at the end
// of s. It returns -1 when every escape sequence is well-formed.
func unterminatedCSI(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			continue
		}
		if i+1 >= len(s) {
			return i
		}
		if s[i+1] != '[' {
			continue // non-CSI escapes (OSC 8 etc.) are not produced in the left pane
		}
		j := i + 2
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f { // parameter + intermediate bytes
			j++
		}
		if j >= len(s) || s[j] < 0x40 || s[j] > 0x7e {
			return i
		}
		i = j
	}
	return -1
}

// assertPaneLinesANSISafe checks every line of a rendered left pane: no broken
// escape sequence and exactly leftPaneWidth display cells.
func assertPaneLinesANSISafe(t *testing.T, pane string) {
	t.Helper()
	for n, line := range strings.Split(pane, "\n") {
		if off := unterminatedCSI(line); off >= 0 {
			t.Errorf("line %d has an unterminated escape at byte %d: %q", n, off, line)
		}
		if w := lipgloss.Width(line); w != leftPaneWidth {
			t.Errorf("line %d is %d cells wide, want %d: %q", n, w, leftPaneWidth, line)
		}
	}
}

// findLine returns the first pane line whose visible text contains needle.
func findLine(pane, needle string) (string, bool) {
	for _, line := range strings.Split(pane, "\n") {
		if strings.Contains(stripANSI(line), needle) {
			return line, true
		}
	}
	return "", false
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestSelectedRowTruncationIsANSISafe(t *testing.T) {
	cases := []struct {
		name       string
		row        server.RunningRow
		profile    string
		wantInLine string // visible text that must survive on the selected row
	}{
		{
			// Fits in 46 cells: the state badge must be fully present.
			name:       "fits_badge_present",
			row:        server.RunningRow{Identifier: "PROJ-1", State: "Running", TurnCount: 3, InputTokens: 1200, OutputTokens: 300},
			wantInLine: "[◉ RUNNING]",
		},
		{
			// Long identifier + long state + profile badge: overflows the pane.
			name:       "overflow_long_fields",
			row:        server.RunningRow{Identifier: "VERYLONGPROJECT-123456789", State: "In Progress Review", TurnCount: 42, InputTokens: 1_500_000, OutputTokens: 250_000},
			profile:    "frontend-specialist",
			wantInLine: "▶ VERYLONGPROJ…",
		},
		{
			// Wide (CJK + emoji) identifier: two cells per rune.
			name:       "wide_rune_identifier",
			row:        server.RunningRow{Identifier: "課題-日本語テスト🚀🚀", State: "In Progress", TurnCount: 7, InputTokens: 90_000, OutputTokens: 10_000},
			profile:    "レビュー担当者",
			wantInLine: "▶ 課題",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forceTrueColor(t)
			m := readyModel(newTestSnap(server.StateSnapshot{Running: []server.RunningRow{tc.row}}))
			if tc.profile != "" {
				m.profileOverrides = map[string]string{tc.row.Identifier: tc.profile}
			}
			if m.selectedNav != 0 {
				t.Fatalf("selectedNav = %d, want 0 (the running row must be selected)", m.selectedNav)
			}
			pane := m.renderLeft()
			assertPaneLinesANSISafe(t, pane)
			line, ok := findLine(pane, "▶ ")
			if !ok {
				t.Fatalf("selected row not found in pane:\n%s", pane)
			}
			if !strings.Contains(stripANSI(line), tc.wantInLine) {
				t.Errorf("selected row %q lacks %q", stripANSI(line), tc.wantInLine)
			}
			if !strings.Contains(line, "\x1b[") {
				t.Errorf("selected row carries no styling; the ANSI path was not exercised: %q", line)
			}
		})
	}
}

func TestProfilePickerRowsAreANSISafe(t *testing.T) {
	// "é" is e + COMBINING ACUTE ACCENT: two runes, three bytes, one cell.
	combining := "café-résumé-profile-with-a-very-long-name"
	items := []string{combining, "backend", "日本語のプロファイル名前がとても長い"}
	for cursor := 0; cursor <= len(items); cursor++ { // len(items) selects the clear-override row
		t.Run(fmt.Sprintf("cursor_%d", cursor), func(t *testing.T) {
			forceTrueColor(t)
			snap := newTestSnap(server.StateSnapshot{
				Running:           []server.RunningRow{{Identifier: "PROJ-1", State: "Running"}},
				AvailableProfiles: items,
				ProfileDefs: map[string]server.ProfileDef{
					combining: {Backend: "claude"},
					"backend": {Backend: "codex"},
				},
			})
			m := readyModel(snap)
			m.profilePickerOpen = true
			m.profilePickerTarget = "PROJ-1"
			m.profilePickerItems = items
			m.profilePickerCursor = cursor
			pane := m.renderLeft()
			assertPaneLinesANSISafe(t, pane)
			if cursor < len(items) {
				want := stripANSI(truncate(items[cursor], leftPaneWidth-10))
				if _, ok := findLine(pane, want); !ok {
					t.Errorf("selected profile %q not rendered in pane:\n%s", want, stripANSI(pane))
				}
			} else if _, ok := findLine(pane, "clear override"); !ok {
				t.Errorf("clear-override row missing:\n%s", stripANSI(pane))
			}
		})
	}
}

func TestTruncateIsDisplayWidthAware(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"abcdefghij", 5, "abcd…"},
		{"日本語テスト", 5, "日本…"}, // 2+2 cells + ellipsis = 5
		{"日本語テスト", 12, "日本語テスト"},
		{"🚀🚀🚀", 4, "🚀…"},
		{"ééé", 3, "ééé"}, // 3 cells, not 9 bytes
		{"abc", 1, "…"},
		{"abc", 0, "…"},
	}
	for _, tc := range cases {
		got := truncate(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
		if w := lipgloss.Width(got); tc.max >= 1 && w > tc.max {
			t.Errorf("truncate(%q, %d) is %d cells wide", tc.in, tc.max, w)
		}
	}
}

// TestLeftPaneSanitizesMultiLineAgentText pins BH-M5-4: agent text and
// subagent descriptions containing '\n', '\r', '\t' or other control bytes
// must render as exactly one pane line each. xansi.StringWidth counts those
// bytes as zero cells, so without sanitizing, truncate() lets them through, one
// logical entry becomes several physical lines, and the pane grows past
// bodyHeight (pushing the footer off-screen).
func TestLeftPaneSanitizesMultiLineAgentText(t *testing.T) {
	forceTrueColor(t)
	const dirty = "Plan:\n\t1. read go.mod\r\n\t2. run\x07 tests\x1b[31m red\x7f"
	buf := logbuffer.New()
	var rows []server.RunningRow
	for i := range 12 {
		id := fmt.Sprintf("PROJ-%d", i)
		rows = append(rows, server.RunningRow{Identifier: id, State: "Running"})
		if i == 1 {
			buf.Add(id, logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "Explore\n\tthe repo\nnow"}))
			continue
		}
		buf.Add(id, logLine("INFO", "claude: text", map[string]string{"session_id": "s1", "text": dirty}))
	}
	m := New(newTestSnap(server.StateSnapshot{Running: rows}), buf, Config{MaxAgents: 5}, func(string) bool { return true })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = newM.(Model)
	newM, _ = m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	if m.lastText["PROJ-0"] == "" || len(m.subagents["PROJ-1"]) != 1 {
		t.Fatalf("fixture not loaded: lastText=%q subagents=%d", m.lastText["PROJ-0"], len(m.subagents["PROJ-1"]))
	}

	for _, sel := range []int{0, 1} { // 0: selected text preview (fitWidth path); 1: others unselected
		m.selectedNav = sel
		pane := m.renderLeft()
		got := strings.Split(pane, "\n")
		if bh := m.bodyHeight(); len(got) > bh {
			t.Errorf("selectedNav=%d: pane has %d lines, bodyHeight is %d", sel, len(got), bh)
		}
		assertPaneLinesANSISafe(t, pane)
		for n, line := range got {
			vis := stripANSI(line)
			if strings.ContainsAny(vis, "\t\r\x07\x7f") {
				t.Errorf("selectedNav=%d line %d carries a control byte: %q", sel, n, vis)
			}
		}
		if _, ok := findLine(pane, "Plan: ⏎ 1. read"); !ok {
			t.Errorf("selectedNav=%d: joined preview not rendered:\n%s", sel, stripANSI(pane))
		}
		if _, ok := findLine(pane, "Explore ⏎ the repo"); !ok {
			t.Errorf("selectedNav=%d: joined subagent description not rendered:\n%s", sel, stripANSI(pane))
		}
	}
}

func TestSingleLineSanitizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"a\nb", "a ⏎ b"},
		{"a\r\n\r\nb\n", "a ⏎ b"},
		{"\n\nlead", "lead"},
		{"tab\there", "tab here"},
		{"bell\x07 esc\x1b[31m del\x7f c1\u0085", "bell esc [31m del c1"},
		{"  spaced   out  ", "spaced out"},
		{"日本\t語", "日本 語"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := singleLine(tc.in); got != tc.want {
			t.Errorf("singleLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
