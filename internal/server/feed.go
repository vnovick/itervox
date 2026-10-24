package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// feed.go — #213: the event feed with a cursor (GET /api/v1/feed) and the
// daemon-wide dispatch pause (POST /api/v1/dispatch/pause|resume).
//
// The feed path is /api/v1/feed so it does not clash with the /events SSE
// stream; a PRD that names /api/v1/events?since= means this endpoint. The
// SSE stream announces new events with a `feed` event carrying the newest
// seq (handleEvents), so a live client reads the feed only when it moved.

// CapabilityDispatchPause is advertised in StateSnapshot.Capabilities when
// the daemon serves the dispatch pause endpoints and the feed (#213).
const CapabilityDispatchPause = "dispatch_pause"

// ErrPauseUntilPast is the DispatchClient counterpart of the orchestrator's
// error for an until that is not in the future (answered 400).
var ErrPauseUntilPast = errors.New("server: until must be in the future")

// feedMaxLimit mirrors orchestrator.FeedMaxLimit: a larger limit is a 400.
const feedMaxLimit = 1000

// pauseReasonMaxBytes bounds the operator's pause reason.
const pauseReasonMaxBytes = 500

// FeedPayload is the small, redacted payload of a feed event. It never
// holds agent output or prompt text.
type FeedPayload struct {
	RunID    string    `json:"runId,omitempty"`
	RunKind  string    `json:"runKind,omitempty"`
	Profile  string    `json:"profile,omitempty"`
	Backend  string    `json:"backend,omitempty"`
	Outcome  string    `json:"outcome,omitempty"`
	Verdict  string    `json:"verdict,omitempty"`
	PRURL    string    `json:"prUrl,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Via      string    `json:"via,omitempty"`
	MemoryID string    `json:"memoryId,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Until    time.Time `json:"until,omitzero"`
	// Count is the line comments of an operator PR review (#238).
	Count int `json:"count,omitempty"`
}

// FeedEvent is one feed entry of GET /api/v1/feed.
type FeedEvent struct {
	Seq        int64       `json:"seq"`
	TS         time.Time   `json:"ts"`
	Kind       string      `json:"kind"`
	Identifier string      `json:"identifier,omitempty"`
	Payload    FeedPayload `json:"payload,omitzero"`
}

// FeedPage is the response of GET /api/v1/feed.
type FeedPage struct {
	Events    []FeedEvent `json:"events"`
	NextSeq   int64       `json:"nextSeq"`
	HeadSeq   int64       `json:"headSeq"`
	OldestSeq int64       `json:"oldestSeq"`
	Truncated bool        `json:"truncated"`
	HasMore   bool        `json:"hasMore"`
}

// DispatchPauseRow is StateSnapshot.DispatchPaused: present (non-null)
// only while all dispatch is paused.
type DispatchPauseRow struct {
	Reason   string     `json:"reason,omitempty"`
	Until    *time.Time `json:"until,omitempty"`
	PausedAt time.Time  `json:"pausedAt"`
}

// DispatchClient backs the feed and the dispatch pause (#213). cmd/itervox
// adapts the orchestrator. PauseDispatch / ResumeDispatch return the pause
// the event loop applied (nil = not paused); ErrBusy when the loop could
// not be reached, ErrPauseUntilPast for an until in the past.
type DispatchClient interface {
	Feed(since int64, limit int) FeedPage
	PauseDispatch(ctx context.Context, reason string, until time.Time) (*DispatchPauseRow, error)
	ResumeDispatch(ctx context.Context) (*DispatchPauseRow, error)
}

// handleFeed serves GET /api/v1/feed?since=<seq>&limit=<n>.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	if s.dispatch == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the event feed is not available")
		return
	}
	q := r.URL.Query()
	var since int64
	if v := q.Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErrorWithField(w, http.StatusBadRequest, "bad_request", "since must be a non-negative integer", "since")
			return
		}
		since = n
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > feedMaxLimit {
			writeErrorWithField(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 1000", "limit")
			return
		}
		limit = n
	}
	page := s.dispatch.Feed(since, limit)
	if page.Events == nil {
		page.Events = []FeedEvent{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
}

// handlePauseDispatch serves POST /api/v1/dispatch/pause
// {"reason":"…","until":"RFC3339"} — both optional, an empty body is fine.
// 200 {"dispatchPaused":{…}} once the event loop applied it.
func (s *Server) handlePauseDispatch(w http.ResponseWriter, r *http.Request) {
	if s.dispatch == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "dispatch pause is not available")
		return
	}
	var body struct {
		Reason string `json:"reason"`
		Until  string `json:"until"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSONBody(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
			return
		}
	}
	reason := strings.TrimSpace(body.Reason)
	if len(reason) > pauseReasonMaxBytes {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "reason is longer than 500 bytes", "reason")
		return
	}
	var until time.Time
	if body.Until != "" {
		t, err := time.Parse(time.RFC3339, body.Until)
		if err != nil {
			writeErrorWithField(w, http.StatusBadRequest, "bad_request", "until must be an RFC 3339 time", "until")
			return
		}
		until = t
	}
	row, err := s.dispatch.PauseDispatch(r.Context(), reason, until)
	s.writePauseResult(w, row, err)
}

// handleResumeDispatch serves POST /api/v1/dispatch/resume.
func (s *Server) handleResumeDispatch(w http.ResponseWriter, r *http.Request) {
	if s.dispatch == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "dispatch pause is not available")
		return
	}
	row, err := s.dispatch.ResumeDispatch(r.Context())
	s.writePauseResult(w, row, err)
}

func (s *Server) writePauseResult(w http.ResponseWriter, row *DispatchPauseRow, err error) {
	switch {
	case errors.Is(err, ErrPauseUntilPast):
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "until must be in the future", "until")
	case err != nil:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "busy", "the orchestrator did not answer; try again")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"dispatchPaused": row})
	}
}
