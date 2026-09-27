package statusui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/vnovick/itervox/internal/server"
)

// Header, footer and narrow-terminal layout helpers (CORE-084, CORE-092),
// split out of model.go.

// View implements tea.Model and renders the full TUI layout.
// renderHeader draws the TUI header. Every line ends in "\n", so
// headerLineCount is the newline count of this string.
func (m *Model) renderHeader(s server.StateSnapshot) string {
	// ── Header (full width) ─────────────────────────────────
	var totalIn, totalOut int
	for _, r := range s.Running {
		totalIn += r.InputTokens
		totalOut += r.OutputTokens
	}

	// ── Angular sci-fi header ────────────────────────────────
	// Top bar: ╔═[ ITERVOX ]═══...═╗
	innerW := max(0, m.width-2)
	title := "[ ITER//VOX ]"
	ruleLen := max(0, innerW-len(title)-1)
	hdrTop := styleGray.Render("╔═") +
		styleCyan.Bold(true).Render(title) +
		styleGray.Render(strings.Repeat("═", ruleLen)+"╗")

	// Backend display: collect unique backends from running sessions
	backendSet := make(map[string]bool)
	for _, r := range s.Running {
		if r.Backend != "" {
			backendSet[r.Backend] = true
		}
	}
	backendPart := ""
	if len(backendSet) > 0 {
		backends := make([]string, 0, len(backendSet))
		for b := range backendSet {
			backends = append(backends, b)
		}
		sort.Strings(backends)
		backendPart = styleGray.Render("   ") +
			styleLabel.Render("BACKEND") + styleGray.Render(" ▸ ") +
			styleCyan.Render(strings.Join(backends, ", "))
	}

	// Agent / token / retry row. Counts and words come from the CORE-076
	// status model (statusmodel.go), shared with the web dashboard.
	sum := summarizeStatus(s)
	agentVal := statusStyle(statusRunning).Render(fmt.Sprintf("%d", sum.Running)) +
		styleGray.Render(fmt.Sprintf("/%d", m.cfg.MaxAgents)) +
		" " + activityStyle(sum.Activity).Render(activityLabel[sum.Activity])
	tokenVal := styleYellow.Render("↑"+fmtCount(totalIn)) +
		styleMuted.Render(" ↓"+fmtCount(totalOut)) +
		styleGray.Render(" ∑"+fmtCount(totalIn+totalOut))
	retryColor := styleMuted
	if sum.Retrying > 0 {
		retryColor = statusStyle(statusRetrying)
	}
	retryVal := retryColor.Render(fmt.Sprintf("%d", sum.Retrying))
	pausedPart := ""
	if sum.Paused > 0 {
		pausedPart = styleGray.Render("   ") +
			styleLabel.Render(statusHeading(statusPaused)) + styleGray.Render(" ▸ ") +
			statusStyle(statusPaused).Render(fmt.Sprintf("%d", sum.Paused))
	}

	row1 := styleGray.Render("║ ") +
		styleLabel.Render("AGENTS") + styleGray.Render(" ▸ ") + agentVal +
		styleGray.Render("   ") +
		styleLabel.Render("TOKENS") + styleGray.Render(" ▸ ") + tokenVal +
		styleGray.Render("   ") +
		styleLabel.Render(statusHeading(statusRetrying)) + styleGray.Render(" ▸ ") + retryVal +
		pausedPart +
		backendPart

	var hdr strings.Builder
	hdr.WriteString(hdrTop + "\n")
	hdr.WriteString(row1 + "\n")

	// CORE-084: the tracker API request budget (not agent quota), when the
	// tracker reports one.
	if line := trackerBudgetHeaderLine(s.RateLimits); line != "" {
		hdr.WriteString(line + "\n")
	}

	if sum.NeedsInput > 0 || sum.Resuming > 0 {
		var parts []string
		if sum.NeedsInput > 0 {
			parts = append(parts, statusStyle(statusInputRequired).Render(
				fmt.Sprintf("%d %s", sum.NeedsInput, statusMeta[statusInputRequired].Noun)))
		}
		if sum.Resuming > 0 {
			parts = append(parts, statusStyle(statusPendingInputResume).Render(
				fmt.Sprintf("%d %s", sum.Resuming, statusMeta[statusPendingInputResume].Noun)))
		}
		hdr.WriteString(styleGray.Render("║ ") +
			styleLabel.Render("INPUT ") + styleGray.Render(" ▸ ") +
			strings.Join(parts, styleGray.Render("   ")) +
			styleMuted.Render("  reply in tracker/dashboard") + "\n")
	}

	if line := backendHealthHeaderLine(s, time.Now(), m.width); line != "" { // CORE-055
		hdr.WriteString(line + "\n")
	}

	if m.cfg.DashboardURL != "" {
		hdr.WriteString(styleGray.Render("║ ") +
			styleLabel.Render("WEB   ") + styleGray.Render(" ▸ ") +
			styleCyan.Render(osc8Link(m.cfg.DashboardURL, m.cfg.DashboardURL)) +
			styleMuted.Render("  w:"+copyKeyHint) + "\n")
	}

	// GitHub tracker info: states are mapped to issue labels
	if m.cfg.TrackerKind == "github" {
		hdr.WriteString(styleGray.Render("║ ") +
			styleMuted.Render("ⓘ GitHub: issue states mapped to labels") + "\n")
	}

	statusMsg := m.killMsg
	if m.dispatchMsg != "" {
		statusMsg = m.dispatchMsg
	}
	if statusMsg != "" {
		hdr.WriteString(styleGray.Render("║ ") + styleYellow.Render("⚡ "+statusMsg) + "\n")
	}

	// Config-invalid banner (T-26 piece 4) — surface a stale-config state to
	// the operator so they know their last WORKFLOW.md edit didn't take and
	// the daemon is running on the previously-valid config. Mirrors the web
	// dashboard banner behavior.
	if s.ConfigInvalid != nil {
		ci := s.ConfigInvalid
		hdr.WriteString(styleGray.Render("║ ") +
			styleRed.Bold(true).Render("⚠ CONFIG INVALID") +
			styleGray.Render(" ▸ ") +
			styleYellow.Render(ci.Error) +
			styleMuted.Render(fmt.Sprintf("  (retry %d, daemon on last valid config)", ci.RetryAttempt)) +
			"\n")
	}

	return hdr.String()
}

// trackerBudgetHeaderLine renders the tracker API request budget row
// (CORE-084), or "" when the tracker reports none. A non-positive limit
// renders "unknown" rather than dividing by it.
func trackerBudgetHeaderLine(rl *server.RateLimitInfo) string {
	if rl == nil {
		return ""
	}
	val := styleMuted.Render("API budget: unknown")
	if rl.RequestsLimit > 0 {
		pct := rl.RequestsRemaining * 100 / rl.RequestsLimit
		style := styleCyan
		if rl.RequestsRemaining*10 < rl.RequestsLimit {
			style = styleYellow
		}
		val = style.Render(fmt.Sprintf("API budget %d%%", pct)) +
			styleMuted.Render(fmt.Sprintf("  %d/%d requests", rl.RequestsRemaining, rl.RequestsLimit))
		if rl.RequestsReset != nil {
			val += styleMuted.Render("  resets " + rl.RequestsReset.Local().Format("15:04"))
		}
	}
	return styleGray.Render("║ ") + styleLabel.Render("TRACKER") + styleGray.Render(" ▸ ") + val
}

// narrow reports whether the terminal is below narrowWidth columns, where
// the TUI shows one pane at a time (CORE-092). Separate from the 120-column
// threshold of the split-details pane.
func (m *Model) narrow() bool { return m.width > 0 && m.width < narrowWidth }

// helpView renders the footer help (short, or full after `?`) at the
// terminal width, so the short line is cut with an ellipsis rather than wrap.
func (m *Model) helpView() string {
	h := m.help
	h.Width = m.width
	return h.View(m.keys)
}

// footerLineCount is the footer's height: the ╚═ rule plus the help block,
// which grows to several rows with the full help (CORE-092).
func (m *Model) footerLineCount() int {
	return 1 + strings.Count(m.helpView(), "\n") + 1
}

// clampLines cuts every line of s to at most width display columns
// (CORE-092's narrow-terminal guarantee), keeping ANSI styling intact.
func clampLines(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if lipgloss.Width(l) > width {
			lines[i] = xansi.Truncate(l, width, "")
		}
	}
	return strings.Join(lines, "\n")
}
