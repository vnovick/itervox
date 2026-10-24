package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/server"
)

// Rendering of `itervox watch` (#240): the status header, the lanes, the
// prompts waiting for a human and the event lines, as text (coloured or
// plain) or as JSON lines.

// paint colours text with ANSI SGR codes when true; plain otherwise.
type paint bool

// SGR codes watch uses.
const (
	sgrBold    = "1"
	sgrDim     = "2"
	sgrRed     = "31"
	sgrGreen   = "32"
	sgrYellow  = "33"
	sgrMagenta = "35"
	sgrCyan    = "36"
)

func (p paint) c(code, s string) string {
	if !p || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// redactLine is agent-derived text made safe for a terminal line: known
// secret shapes redacted (before any cut, so a cut secret cannot slip
// through), control characters dropped, whitespace folded.
func redactLine(s string) string {
	s = logging.RedactString(s)
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' ' // no escape sequence from agent text reaches the terminal
		}
		return r
	}, s)
	return oneLine(s)
}

// shellWord quotes s for a POSIX shell when it needs it (a GitHub "#12"
// would otherwise start a comment).
func shellWord(s string) string {
	shellSafe := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:@%+=,", r)
	}
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !shellSafe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// command is an itervox command line that reaches the watched daemon.
func (w *watcher) command(verb, identifier string, rest ...string) string {
	parts := []string{"itervox", verb}
	for _, a := range w.opt.target {
		parts = append(parts, shellWord(a))
	}
	parts = append(parts, shellWord(identifier))
	return strings.Join(append(parts, rest...), " ")
}

// replyCommand answers an input request.
func (w *watcher) replyCommand(identifier string) string {
	return w.command("reply", identifier, `"<your answer>"`)
}

// approveCommand answers a guardrail approval request.
func (w *watcher) approveCommandLine(identifier string) string {
	return w.command("approve", identifier)
}

// prompt is one request waiting for a human.
type prompt struct {
	Identifier string `json:"identifier"`
	// Kind is "input" (a question) or "approval" (a guardrail approval).
	Kind    string `json:"kind"`
	Context string `json:"context"`
	// Command answers it; Reject (approvals only) refuses it with a reason.
	Command string `json:"command"`
	Reject  string `json:"reject,omitempty"`
}

func (w *watcher) prompts(st *watchState) []prompt {
	var out []prompt
	for _, in := range st.InputRequired {
		if !in.waiting() {
			continue
		}
		if in.GuardrailApproval {
			first, _, _ := strings.Cut(strings.TrimSpace(in.Context), "\n")
			out = append(out, prompt{Identifier: in.Identifier, Kind: "approval", Context: clip(redactLine(first), 120),
				Command: w.approveCommandLine(in.Identifier), Reject: w.command("reply", in.Identifier, `"<why not>"`)})
			continue
		}
		out = append(out, prompt{Identifier: in.Identifier, Kind: "input", Context: clip(redactLine(in.Context), 120),
			Command: w.replyCommand(in.Identifier)})
	}
	return out
}

// renderPrompts is the NEEDS YOU block.
func (w *watcher) renderPrompts(b *strings.Builder, ps []prompt) {
	if len(ps) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n", w.p.c(sgrBold+";"+sgrYellow, "NEEDS YOU"))
	for _, pr := range ps {
		label, mark := "question", "?"
		if pr.Kind == "approval" {
			label, mark = "approval", "!"
		}
		fmt.Fprintf(b, "  %s %-10s %s: %s\n", w.p.c(sgrBold+";"+sgrYellow, mark), pr.Identifier, label, pr.Context)
		fmt.Fprintf(b, "      %s %s\n", w.p.c(sgrYellow, "→"), w.p.c(sgrBold, pr.Command))
		if pr.Reject != "" {
			fmt.Fprintf(b, "      %s\n", w.p.c(sgrDim, "to refuse it: "+pr.Reject))
		}
	}
}

// laneWho is a lane's "profile · backend reviewer @host".
func laneWho(r watchRunning) string {
	who := r.Backend
	if r.Profile != "" {
		who = r.Profile + " · " + cmpStr(r.Backend, "?")
	}
	if r.Kind != "" && r.Kind != "worker" {
		who += " " + r.Kind
	}
	if r.WorkerHost != "" {
		who += " @" + r.WorkerHost
	}
	return who
}

// renderStatus is the header and the lanes (and, unless issueOnly, the
// prompts and the other queues). Without live, the fields that change on
// every poll (elapsed time, tokens, cost, retry due times) are left out:
// that view is what the stream modes compare to decide whether to print
// again.
func (w *watcher) renderStatus(s *watchState, now time.Time, live bool) string {
	var b strings.Builder
	p := w.p
	slotBound := s.DispatchPressure.EligibleWaiting > 0 && len(s.Running) >= s.MaxConcurrentAgents
	depBound := s.DispatchPressure.BlockedByDependency > 0 && len(s.Running) < s.MaxConcurrentAgents
	prompts := w.prompts(s)
	fmt.Fprintf(&b, "%s · %d/%d running · %d queued · %d retrying · %d waiting for you · %d waiting for a slot · %d blocked by dependencies · %d paused\n",
		p.c(sgrBold, "itervox"), len(s.Running), s.MaxConcurrentAgents, len(s.AutomationQueue), len(s.Retrying), len(prompts),
		s.DispatchPressure.EligibleWaiting, s.DispatchPressure.BlockedByDependency, len(s.Paused))
	switch {
	case slotBound:
		b.WriteString(p.c(sgrYellow, "slot-bound: every slot is busy and work is waiting (itervox workers N adds slots)") + "\n")
	case depBound:
		b.WriteString(p.c(sgrYellow, "dependency-bound: slots are free, but the waiting work is blocked by its dependencies") + "\n")
	}
	if dp := s.DispatchPaused; dp != nil { // #213
		line := "ALL DISPATCH PAUSED — running agents continue, nothing new starts"
		if dp.Reason != "" {
			line += ": " + redactLine(dp.Reason)
		}
		b.WriteString(p.c(sgrBold+";"+sgrMagenta, line) + " (itervox resume)\n")
	}
	if w.opt.hubLine != nil {
		if h := w.opt.hubLine(); h != "" {
			b.WriteString(p.c(sgrCyan, "hub: "+h) + "\n")
		}
	}
	if len(s.Running) > 0 {
		b.WriteString("\n")
	}
	for _, r := range s.Running {
		if w.opt.issue != "" && r.Identifier != w.opt.issue {
			continue
		}
		elapsed, tokens, cost := "", "", ""
		if live {
			elapsed = shortDuration(time.Duration(r.ElapsedMs) * time.Millisecond)
			tokens = fmtTokens(r.Tokens) + " tok"
			if r.CostUSDEstimated != nil {
				cost = fmt.Sprintf("$%.2f", *r.CostUSDEstimated)
			}
		}
		colour := sgrGreen
		if r.Kind == "reviewer" {
			colour = sgrCyan
		}
		lane := fmt.Sprintf("%s %s %-36s %-24s %7s  turn %-3d %9s %6s", p.c(colour, "●"), p.c(sgrBold, fmt.Sprintf("%-10s", r.Identifier)),
			clip(redactLine(cmpStr(r.Title, r.State)), 36), clip(laneWho(r), 24), elapsed, r.TurnCount, tokens, cost)
		b.WriteString(strings.TrimRight(lane, " ") + "\n")
		if a := w.activity[r.Identifier].line; a != "" {
			fmt.Fprintf(&b, "    %s %s\n", p.c(sgrDim, "›"), clip(a, 110))
		}
	}
	if w.opt.issue != "" {
		return b.String()
	}
	w.renderPrompts(&b, prompts)
	section := func(title string, n int) bool {
		if n == 0 {
			return false
		}
		fmt.Fprintf(&b, "\n%s\n", p.c(sgrBold, title))
		return true
	}
	if section("RETRYING", len(s.Retrying)) {
		for _, r := range s.Retrying {
			in := ""
			if live {
				in = "now"
				if d := r.DueAt.Sub(now); d > 0 {
					in = "in " + shortDuration(d)
				}
			}
			fmt.Fprintf(&b, "  %s attempt %-3d %-9s %s\n", p.c(sgrYellow, fmt.Sprintf("%-10s", r.Identifier)), r.Attempt, in, clip(redactLine(r.Error), 70))
		}
	}
	if section("QUEUED AUTOMATIONS", len(s.AutomationQueue)) {
		for _, q := range s.AutomationQueue {
			fmt.Fprintf(&b, "  %-10s %-24s %s\n", q.Identifier, clip(q.AutomationID, 24), strings.ReplaceAll(q.Reason, "_", " "))
		}
	}
	if section("PAUSED", len(s.Paused)) {
		for _, id := range s.Paused {
			line := "  " + p.c(sgrMagenta, fmt.Sprintf("%-10s", id))
			if reason := s.PauseReasons[id]; reason != "" {
				line += " " + strings.ReplaceAll(reason, "_", " ")
			}
			b.WriteString(strings.TrimRight(line, " ") + "\n")
		}
	}
	if len(s.Running)+len(s.Retrying)+len(s.InputRequired)+len(s.AutomationQueue)+len(s.Paused)+s.DispatchPressure.EligibleWaiting == 0 {
		b.WriteString("\nNothing running or waiting.\n")
	}
	return b.String()
}

// emitText is the stream mode (no terminal, --events-only, --once): the
// status when it changed, with a time stamp, then each event once.
func (w *watcher) emitText(st *watchState, stErr error, events []server.FeedEvent, gap *feedGap, now time.Time) {
	switch {
	case st != nil:
		if key := w.renderStatus(st, now, false); key != w.lastKey || w.opt.once {
			w.lastKey = key
			view := w.renderStatus(st, now, true)
			if w.opt.once {
				_, _ = io.WriteString(w.out, view)
			} else {
				_, _ = fmt.Fprintf(w.out, "--- %s ---\n%s", now.Format(time.RFC3339), view)
			}
		}
	case stErr != nil:
		msg := "itervox watch: " + stErr.Error() + " (retrying)\n"
		if msg != w.lastKey && !w.opt.once {
			w.lastKey = msg
			_, _ = io.WriteString(w.out, msg)
		}
	}
	if len(events) > 0 && st != nil && !w.opt.eventsOnly {
		_, _ = fmt.Fprintln(w.out)
	}
	w.printEvents(events, gap)
}

// drawPanel is the terminal panel: cleared and redrawn in place each tick
// (a plain clear-screen sequence; the terminal's mode is never touched).
func (w *watcher) drawPanel(st *watchState, stErr error, events []server.FeedEvent, gap *feedGap, now time.Time) {
	if gap != nil {
		w.recent = append(w.recent, w.p.c(sgrDim, gapText(gap)))
	}
	for _, ev := range events {
		if w.wantEvent(ev) {
			line, _ := w.eventText(ev)
			w.recent = append(w.recent, line)
		}
	}
	if n := len(w.recent) - watchRecentEvents; n > 0 {
		w.recent = append([]string(nil), w.recent[n:]...)
	}
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	cursor := ""
	if w.cursor >= 0 && !w.feedOff {
		cursor = fmt.Sprintf(" · feed #%d", w.cursor)
	}
	b.WriteString(w.p.c(sgrDim, now.Format("15:04:05")+"  itervox watch · Ctrl-C to quit"+cursor) + "\n")
	switch {
	case st != nil:
		b.WriteString(w.renderStatus(st, now, true))
	case stErr != nil:
		b.WriteString(w.p.c(sgrRed, "itervox watch: "+stErr.Error()+" (retrying)") + "\n")
	}
	if len(w.recent) > 0 {
		fmt.Fprintf(&b, "\n%s\n", w.p.c(sgrBold, "EVENTS"))
		for _, l := range w.recent {
			b.WriteString("  " + l + "\n")
		}
	}
	_, _ = io.WriteString(w.out, b.String())
}
