package server

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// needs_you.go — the "Needs you" lane: every issue whose run is blocked on
// a person, oldest first, each with the one question or approval it waits
// on and since when. It is derived from the snapshot's input-required rows
// (no event-loop state of its own): a waiting question, a guardrail
// approval and a held pipeline push (guardrails.lanes.human). Answered
// questions that are only resuming (pending_input_resume) wait on nobody
// and are left out. The dashboard lane, `itervox watch` and the desktop app
// (directly or through the hub's copy of GET /api/v1/summary) all read it.

// Needs-you kinds.
const (
	NeedsYouQuestion       = "question"
	NeedsYouApproval       = "approval"
	NeedsYouPipelineChange = "pipeline-change"
)

// needsYouSummaryRunes caps a row's one-line summary.
const needsYouSummaryRunes = 200

// summaryNeedsYouMax caps the rows GET /api/v1/summary carries (the oldest
// first); the counts stay exact.
const summaryNeedsYouMax = 20

// needsYouSentinel is the marker agents print to ask for input
// (agent.InputRequiredSentinel); it is noise in a summary line.
const needsYouSentinel = "<!-- itervox:needs-input -->"

// NeedsYouRow is one item of the "Needs you" lane.
type NeedsYouRow struct {
	// Kind is question, approval or pipeline-change.
	Kind       string `json:"kind"`
	Identifier string `json:"identifier"`
	// Title is the issue's title when the daemon knows it (a running issue
	// or one in the run history).
	Title string `json:"title,omitempty"`
	// Project is the daemon's project name (StateSnapshot.ProjectName).
	Project   string `json:"project,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	// Summary is the question or approval as one line: the first line of
	// the request, secrets redacted, control characters dropped, at most
	// 200 characters.
	Summary string `json:"summary"`
	// WaitingSince is when the request started waiting (RFC 3339; the
	// input-required entry's queued time, persisted with the entry). Empty
	// when unknown; such rows sort last.
	WaitingSince string `json:"waitingSince,omitempty"`
}

// BuildNeedsYou derives the lane from the snapshot's input-required rows,
// oldest first (ties by identifier). titles maps an identifier to its issue
// title; project is the daemon's project name.
func BuildNeedsYou(rows []InputRequiredRow, titles map[string]string, project string) []NeedsYouRow {
	type keyed struct {
		row NeedsYouRow
		at  time.Time
	}
	items := make([]keyed, 0, len(rows))
	for _, r := range rows {
		if r.State != "" && r.State != inputStateWaiting {
			continue
		}
		kind := NeedsYouQuestion
		switch {
		case len(r.GuardrailLanePaths) > 0:
			kind = NeedsYouPipelineChange
		case r.GuardrailApproval:
			kind = NeedsYouApproval
		}
		at, err := time.Parse(time.RFC3339, r.QueuedAt)
		since := r.QueuedAt
		if err != nil || at.IsZero() || at.Year() <= 1 {
			at, since = time.Time{}, ""
		}
		items = append(items, keyed{at: at, row: NeedsYouRow{
			Kind: kind, Identifier: r.Identifier, Title: needsYouLine(titles[r.Identifier], needsYouSummaryRunes),
			Project: project, SessionID: r.SessionID, Summary: needsYouSummary(r.Context), WaitingSince: since,
		}})
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.at.IsZero() != b.at.IsZero() {
			return !a.at.IsZero()
		}
		if !a.at.Equal(b.at) {
			return a.at.Before(b.at)
		}
		return a.row.Identifier < b.row.Identifier
	})
	out := make([]NeedsYouRow, len(items))
	for i, it := range items {
		out[i] = it.row
	}
	return out
}

// needsYouSummary is the request's first non-empty line, made safe for any
// scope: secrets redacted (redactSecretString, the below-admin redaction)
// before the cut, control characters dropped, whitespace folded.
func needsYouSummary(context string) string {
	context = strings.ReplaceAll(context, needsYouSentinel, " ")
	for line := range strings.SplitSeq(context, "\n") {
		if s := needsYouLine(line, needsYouSummaryRunes); s != "" {
			return s
		}
	}
	return ""
}

// needsYouLine redacts s, drops control characters, folds whitespace and
// cuts it to n runes (with an ellipsis).
func needsYouLine(s string, n int) string {
	s = redactSecretString(s)
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
