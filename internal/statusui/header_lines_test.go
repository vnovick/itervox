package statusui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vnovick/itervox/internal/server"
)

// TestHeaderLineCountMatchesRenderedHeader (CORE-084): headerLineCount
// sizes the body, so it must equal the header View actually draws. It used
// to reserve a rate-limit line that was never rendered (bodyHeight one line
// short whenever the tracker reports a budget) and a spare base line. Every
// branch of headerLineCount is covered: RateLimits nil / set (with a limit,
// and with requestsLimit 0 → "unknown"), DashboardURL empty / set, killMsg
// or dispatchMsg set, InputRequired empty / non-empty.
func TestHeaderLineCountMatchesRenderedHeader(t *testing.T) {
	reset := time.Date(2026, 9, 27, 12, 30, 0, 0, time.UTC)
	rateCases := map[string]*server.RateLimitInfo{
		"nil":     nil,
		"budget":  {RequestsLimit: 1000, RequestsRemaining: 42, RequestsReset: &reset},
		"unknown": {RequestsLimit: 0, RequestsRemaining: 0},
	}
	const width, height = 160, 60
	for rateName, rl := range rateCases {
		for _, url := range []string{"", "http://127.0.0.1:8090/"} {
			for _, msg := range []string{"", "kill", "dispatch"} {
				for _, input := range []bool{false, true} {
					name := fmt.Sprintf("rate=%s/url=%t/msg=%s/input=%t", rateName, url != "", msg, input)
					t.Run(name, func(t *testing.T) {
						snap := server.StateSnapshot{RateLimits: rl}
						if input {
							snap.InputRequired = []server.InputRequiredRow{{Identifier: "ENG-1", State: "input_required"}}
						}
						m := New(newTestSnap(snap), nil, Config{MaxAgents: 5, DashboardURL: url}, func(string) bool { return true })
						next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
						m = next.(Model)
						switch msg {
						case "kill":
							m.killMsg = "⚙ something happened"
						case "dispatch":
							m.dispatchMsg = "⚡ Queued ENG-2"
						}

						lines := strings.Split(strings.TrimSuffix(ansi.Strip(m.View()), "\n"), "\n")
						header := 0
						for _, l := range lines {
							if !strings.HasPrefix(l, "╔") && !strings.HasPrefix(l, "║") {
								break
							}
							header++
						}
						if got, want := header, m.headerLineCount(); got != want {
							t.Fatalf("rendered header lines = %d, headerLineCount() = %d\n%s", got, want, strings.Join(lines[:min(len(lines), header+1)], "\n"))
						}
						if len(lines) != height {
							t.Fatalf("View is %d lines for a %d-line terminal (header sizing is off)", len(lines), height)
						}
						if rl != nil {
							view := strings.Join(lines[:header], "\n")
							want := "API budget 4%"
							if rl.RequestsLimit <= 0 {
								want = "API budget: unknown"
							}
							if !strings.Contains(view, want) {
								t.Fatalf("tracker budget row %q missing:\n%s", want, view)
							}
						}
					})
				}
			}
		}
	}
}
