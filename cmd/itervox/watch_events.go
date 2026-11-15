package main

import (
	"fmt"
	"strings"

	"github.com/vnovick/itervox/internal/server"
)

// Feed events in `itervox watch` (#240): one line per event of the
// daemon's feed (#213), with the command that answers an input request.

// eventText is one feed event as a line, and the command it asks for
// (input requests only).
func (w *watcher) eventText(ev server.FeedEvent) (line, command string) {
	p := w.p
	pl := ev.Payload
	who := strings.Trim(strings.Join(nonEmptyStrings(pl.Profile, pl.Backend, kindLabel(pl.RunKind)), " · "), " ")
	what, colour := strings.ReplaceAll(ev.Kind, "_", " "), ""
	switch ev.Kind {
	case "run_started":
		what = "started"
	case "run_finished":
		what, colour = "finished", sgrGreen
		if pl.Outcome != "" {
			what += ": " + pl.Outcome
		}
	case "run_failed":
		what, colour = "failed", sgrRed
		if pl.Outcome != "" {
			what += ": " + pl.Outcome
		}
	case "input_required":
		colour = sgrBold + ";" + sgrYellow
		if pl.Kind == "guardrail_approval" {
			what, command = "wants approval to run a held command", w.approveCommandLine(ev.Identifier)
		} else {
			what, command = "is waiting for your input", w.replyCommand(ev.Identifier)
		}
	case "input_answered":
		what = "input answered"
		if pl.Via != "" {
			what += " via " + pl.Via
		}
	case "review_verdict":
		what = "review verdict: " + cmpStr(pl.Verdict, "?")
		if pl.Verdict == "approve" {
			colour = sgrGreen
		} else {
			colour = sgrYellow
		}
	case "pr_opened":
		what, colour = "opened a pull request "+pl.PRURL, sgrCyan
	case "pr_merged":
		what, colour = "pull request merged "+pl.PRURL, sgrGreen
	case "guardrail_blocked":
		what, colour = "guardrail refused a command", sgrRed
		if pl.Kind != "" {
			what += " (" + pl.Kind + ")"
		}
	case "guardrail_approved":
		what = "held command approved"
	case "lesson_proposed":
		what = "proposed a lesson"
		if pl.MemoryID != "" {
			what += " (" + pl.MemoryID + ")"
		}
	case "dispatch_paused":
		what, colour = "all dispatch paused", sgrMagenta
		if pl.Reason != "" {
			what += ": " + pl.Reason
		}
	case "dispatch_resumed":
		what = "dispatch resumed"
	}
	what = redactLine(what)
	if who != "" && ev.Kind != "dispatch_paused" && ev.Kind != "dispatch_resumed" {
		what += p.c(sgrDim, " ("+who+")")
	}
	id := ev.Identifier
	if id == "" {
		id = "-"
	}
	line = fmt.Sprintf("%s %s %-10s %s", p.c(sgrDim, ev.TS.Local().Format("15:04:05")), p.c(sgrDim, fmt.Sprintf("#%d", ev.Seq)), id, p.c(colour, what))
	if command != "" {
		line += "  " + p.c(sgrYellow, "→ ") + p.c(sgrBold, command)
	}
	return line, command
}

func kindLabel(k string) string {
	if k == "worker" {
		return ""
	}
	return k
}

func nonEmptyStrings(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// gapText reports events that left the feed window unread.
func gapText(g *feedGap) string {
	return fmt.Sprintf("… events %d–%d left the daemon's feed before they were read; continuing from %d", g.after+1, g.oldest-1, g.oldest)
}

// wantEvent filters the events to --issue.
func (w *watcher) wantEvent(ev server.FeedEvent) bool {
	return w.opt.issue == "" || ev.Identifier == w.opt.issue
}

// printEvents prints events as lines (or JSON lines).
func (w *watcher) printEvents(events []server.FeedEvent, gap *feedGap) {
	if gap != nil {
		if w.opt.json {
			w.jsonLine(map[string]any{"type": "gap", "after": gap.after, "oldestSeq": gap.oldest})
		} else {
			_, _ = fmt.Fprintln(w.out, w.p.c(sgrDim, gapText(gap)))
		}
	}
	for _, ev := range events {
		if !w.wantEvent(ev) {
			continue
		}
		line, command := w.eventText(ev)
		if w.opt.json {
			w.jsonLine(watchEventJSON{Type: "event", FeedEvent: ev, Text: stripSGR(line), Command: command})
			continue
		}
		_, _ = fmt.Fprintln(w.out, line)
	}
}
