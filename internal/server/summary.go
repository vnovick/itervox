package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// summary.go — GET /api/v1/summary (#212): a few hundred bytes a supervisor
// (the Mac menu-bar app) polls instead of the full /api/v1/state. Its ETag
// changes whenever anything the supervisor shows changes, so an idle daemon
// answers a conditional request with 304 and no body, and the supervisor
// skips the /state read entirely.

// inputStateWaiting is InputRequiredRow.State for an agent waiting on a
// human (the other value, "pending_input_resume", is already answered).
const inputStateWaiting = "input_required"

// SummaryCounts are the per-state counts of GET /api/v1/summary.
type SummaryCounts struct {
	Running  int `json:"running"`
	Retrying int `json:"retrying"`
	// Paused counts paused issues (StateSnapshot.Counts.Paused); the
	// daemon-wide pause is Summary.Paused.
	Paused int `json:"paused"`
	// InputRequired counts agents waiting for a human answer.
	InputRequired int `json:"inputRequired"`
	// PendingInputResume counts answered questions whose agent is resuming.
	PendingInputResume int `json:"pendingInputResume"`
	// RecentFailures is the length of the recentFailures ring.
	RecentFailures int `json:"recentFailures"`
}

// Summary is the payload of GET /api/v1/summary.
type Summary struct {
	// ETag is the response's ETag header without quotes. It is a digest of
	// every field below except GeneratedAt, plus the identity of each
	// input-required entry and recent failure, so a new question or failure
	// changes it even when the counts do not.
	ETag        string        `json:"etag"`
	Version     string        `json:"version"`
	ProjectName string        `json:"projectName,omitempty"`
	Counts      SummaryCounts `json:"counts"`
	// Paused is the daemon-wide dispatch pause (#213): true while nothing
	// new starts. PauseReason and PausedUntil describe it.
	Paused      bool       `json:"paused"`
	PauseReason string     `json:"pauseReason,omitempty"`
	PausedUntil *time.Time `json:"pausedUntil,omitempty"`
	GeneratedAt time.Time  `json:"generatedAt"`
}

// summaryDigestInput is what the ETag is computed over. It never holds
// GeneratedAt or other wall-clock-derived values (AgeMinutes, Stale), which
// would change the tag on every request.
type summaryDigestInput struct {
	Version     string
	ProjectName string
	Counts      SummaryCounts
	Paused      bool
	PauseReason string
	PausedUntil *time.Time
	Input       []summaryDigestInputRow
	Failures    []summaryDigestFailure
}

type summaryDigestInputRow struct {
	Identifier, SessionID, State, Context, QueuedAt string
}

type summaryDigestFailure struct {
	Kind, Identifier, Source, Message string
	OccurredAt                        time.Time
	Count                             int
}

// buildSummary derives the summary of snap for a daemon at version.
func buildSummary(snap StateSnapshot, version string) Summary {
	sum := Summary{
		Version:     version,
		ProjectName: snap.ProjectName,
		Counts: SummaryCounts{
			Running:        snap.Counts.Running,
			Retrying:       snap.Counts.Retrying,
			Paused:         snap.Counts.Paused,
			RecentFailures: len(snap.RecentFailures),
		},
		GeneratedAt: snap.GeneratedAt,
	}
	if p := snap.DispatchPaused; p != nil { // #213
		sum.Paused, sum.PauseReason, sum.PausedUntil = true, p.Reason, p.Until
	}
	in := summaryDigestInput{Version: version, ProjectName: snap.ProjectName}
	for _, r := range snap.InputRequired {
		if r.State == inputStateWaiting {
			sum.Counts.InputRequired++
		} else {
			sum.Counts.PendingInputResume++
		}
		in.Input = append(in.Input, summaryDigestInputRow{r.Identifier, r.SessionID, r.State, r.Context, r.QueuedAt})
	}
	for _, f := range snap.RecentFailures {
		in.Failures = append(in.Failures, summaryDigestFailure{f.Kind, f.Identifier, f.Source, f.Message, f.OccurredAt, f.Count})
	}
	in.Counts, in.Paused, in.PauseReason, in.PausedUntil = sum.Counts, sum.Paused, sum.PauseReason, sum.PausedUntil
	raw, _ := json.Marshal(in) // plain strings, ints and times: cannot fail
	digest := sha256.Sum256(raw)
	sum.ETag = hex.EncodeToString(digest[:16])
	return sum
}

// etagMatches reports whether an If-None-Match header value names etag
// (unquoted). It accepts a list, weak validators and "*" (RFC 9110 §13.1.2).
func etagMatches(ifNoneMatch, etag string) bool {
	for part := range strings.SplitSeq(ifNoneMatch, ",") {
		tag := strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if tag == "*" || tag == `"`+etag+`"` {
			return true
		}
	}
	return false
}

// handleSummary serves GET /api/v1/summary from the same snapshot as
// /api/v1/state (never cfgMu), answering 304 when If-None-Match names the
// current ETag.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	sum := buildSummary(s.snapshot(), s.version)
	w.Header().Set("ETag", `"`+sum.ETag+`"`)
	// Cacheable only with revalidation: generatedAt is outside the tag.
	w.Header().Set("Cache-Control", "no-cache")
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, sum.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}
