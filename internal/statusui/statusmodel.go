package statusui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vnovick/itervox/internal/server"
)

// CORE-076 — the TUI half of the single status model. This file mirrors
// web/src/lib/statusModel.ts (STATUS_META, ACTIVITY_LABEL, summarizeStatus):
// the same words, tones and inclusion rules, so the terminal and the dashboard
// never report two different numbers or two words for the same state.
// TestStatusLabelParity parses the TypeScript source and fails on divergence.
//
//   - NeedsInput counts input_required rows only; pending_input_resume rows
//     are Resuming (the reply is in, nothing for the operator to do).
//   - Counts come from the row lists, not server.Counts.
//   - Activity is "Active" while any agent runs, otherwise "Waiting". The TUI
//     always has an in-process snapshot, so the web's "offline" never applies.

type statusKey string

const (
	statusRunning            statusKey = "running"
	statusPaused             statusKey = "paused"
	statusRetrying           statusKey = "retrying"
	statusInputRequired      statusKey = "input_required"
	statusPendingInputResume statusKey = "pending_input_resume"
	statusIdle               statusKey = "idle"
)

type statusTone string

type statusMetaEntry struct {
	// Label is the title-case form for headings (upper-cased in the TUI chrome).
	Label string
	// Noun is the lower-case form for "N <noun>" counts.
	Noun string
	Tone statusTone
}

var statusMeta = map[statusKey]statusMetaEntry{
	statusRunning:            {Label: "Running", Noun: "running", Tone: "success"},
	statusPaused:             {Label: "Paused", Noun: "paused", Tone: "warning"},
	statusRetrying:           {Label: "Retrying", Noun: "retrying", Tone: "warning"},
	statusInputRequired:      {Label: "Needs input", Noun: "needs input", Tone: "warning"},
	statusPendingInputResume: {Label: "Resuming", Noun: "resuming", Tone: "info"},
	statusIdle:               {Label: "Idle", Noun: "idle", Tone: "neutral"},
}

type statusActivity string

const (
	activityActive  statusActivity = "active"
	activityWaiting statusActivity = "waiting"
	activityOffline statusActivity = "offline"
)

var activityLabel = map[statusActivity]string{
	activityActive:  "Active",
	activityWaiting: "Waiting",
	activityOffline: "Offline",
}

// inputRowState mirrors how the web counts a row after parsing: the dashboard
// reads every input row through InputRequiredEntrySchema, whose `state` is
// tolerantEnum([...], 'input_required'), so a missing or unknown state is
// input_required and only an explicit pending_input_resume is Resuming. The
// context-prefix fallback in web inputRequiredRowState never sees a parsed row
// without a state, so the TUI does not apply it either.
// TestStatusLabelParity pins the web schema default.
func inputRowState(row server.InputRequiredRow) statusKey {
	if statusKey(row.State) == statusPendingInputResume {
		return statusPendingInputResume
	}
	return statusInputRequired
}

type statusSummary struct {
	Running    int
	Paused     int
	Retrying   int
	NeedsInput int // input_required rows only
	Resuming   int // pending_input_resume rows
	Activity   statusActivity
}

// summarizeStatus mirrors web summarizeStatus for a present snapshot. The web
// headline (one state for the header chip) has no TUI surface and is omitted.
func summarizeStatus(s server.StateSnapshot) statusSummary {
	out := statusSummary{
		Running:  len(s.Running),
		Paused:   len(s.Paused),
		Retrying: len(s.Retrying),
		Activity: activityWaiting,
	}
	for _, row := range s.InputRequired {
		if inputRowState(row) == statusPendingInputResume {
			out.Resuming++
		} else {
			out.NeedsInput++
		}
	}
	if out.Running > 0 {
		out.Activity = activityActive
	}
	return out
}

// toneStyle maps a status tone to the TUI palette (the web's toneColor).
func toneStyle(t statusTone) lipgloss.Style {
	switch t {
	case "success":
		return styleGreen
	case "warning":
		return styleYellow
	case "danger":
		return styleRed
	case "info":
		return styleCyan
	default:
		return styleMuted
	}
}

// statusHeading is the upper-case label used in the TUI chrome, e.g. "NEEDS INPUT".
func statusHeading(k statusKey) string { return strings.ToUpper(statusMeta[k].Label) }

// statusStyle is the tone style for a state.
func statusStyle(k statusKey) lipgloss.Style { return toneStyle(statusMeta[k].Tone) }

// activityStyle colours the Active/Waiting word next to the agent count.
func activityStyle(a statusActivity) lipgloss.Style {
	if a == activityActive {
		return statusStyle(statusRunning)
	}
	return styleMuted
}
