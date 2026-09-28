package statusui

import (
	"fmt"
	"strings"
	"time"

	xansi "github.com/charmbracelet/x/ansi"

	"github.com/vnovick/itervox/internal/server"
)

// backendHealthHeaderLine renders the CORE-055 "LIMITS" header row: every
// agent backend breaker that is not healthy (limited, probing, warning),
// labelled by backend (and SSH host) so it cannot be read as the tracker
// API budget, plus the auto-switched issue count. "" when every backend is
// healthy and nothing is auto-switched — the row then takes no header
// space, so healthy renders are unchanged. Times are local HH:MM; an
// unknown reset says so rather than showing the cooldown as a reset.
func backendHealthHeaderLine(s server.StateSnapshot, now time.Time, width int) string {
	var parts []string
	for _, r := range s.BackendHealth {
		name := r.Backend
		if r.Host != "" {
			name += "@" + r.Host
		}
		var part string
		switch r.Status {
		case server.BackendHealthLimited:
			switch {
			case r.LimitedUntil != nil:
				part = styleRed.Render(name+" limited") + styleYellow.Render(" until "+resetClock(*r.LimitedUntil, now))
			case r.RetryAt != nil:
				part = styleRed.Render(name+" limited") + styleYellow.Render(" (reset unknown, retry "+resetClock(*r.RetryAt, now)+")")
			default:
				part = styleRed.Render(name+" limited") + styleYellow.Render(" (reset unknown)")
			}
		case server.BackendHealthProbing:
			part = styleYellow.Render(name + " probing")
			if r.ProbeIssue != "" {
				part += styleMuted.Render(" (probe " + r.ProbeIssue + ")")
			}
		case server.BackendHealthWarning:
			part = styleYellow.Render(name + " retrying rate limits")
		default:
			continue
		}
		if r.HeldIssues > 0 || r.ReroutedIssues > 0 {
			part += styleMuted.Render(fmt.Sprintf(" · %d held, %d rerouted", r.HeldIssues, r.ReroutedIssues))
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 && len(s.AutoSwitches) == 0 {
		return ""
	}
	line := styleGray.Render("║ ") + styleLabel.Render("LIMITS") + styleGray.Render(" ▸ ")
	if len(parts) == 0 {
		line += styleGreen.Render("backends healthy")
	} else {
		line += strings.Join(parts, styleGray.Render("   "))
	}
	if n := len(s.AutoSwitches); n > 0 {
		line += styleMuted.Render(fmt.Sprintf("   %d auto-switched", n))
	}
	// BH-M3-9: several breakers (or a long host name) must not wrap the
	// header and shift the panes; the full detail is on the dashboard and in
	// HEARTBEAT.md.
	if width > 0 && xansi.StringWidth(line) > width {
		line = xansi.Truncate(line, width, "…")
	}
	return line
}

// resetClock renders a reset as local HH:MM when it is today, and with the
// date otherwise (BH-M3-9: a seven_day reset must not read as today).
func resetClock(t, now time.Time) string {
	lt, ln := t.Local(), now.Local()
	if lt.Year() == ln.Year() && lt.YearDay() == ln.YearDay() {
		return lt.Format("15:04")
	}
	return lt.Format("Jan 2 15:04")
}
