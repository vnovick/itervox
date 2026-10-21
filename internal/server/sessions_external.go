package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// Sessions panel step 3 (#178): sessions started by hand. They are listed
// by GET /api/v1/sessions with owner "external" and are read-only: their
// transcript is served here.

// TranscriptEntryRow is one transcript item. Text is redacted and
// abbreviated by the daemon.
type TranscriptEntryRow struct {
	// Role is "user" or "assistant".
	Role string `json:"role"`
	// Kind is "text", "tool" (Text: the tool's name) or "tool_result".
	Kind      string     `json:"kind"`
	Text      string     `json:"text"`
	At        *time.Time `json:"at,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// TranscriptResponse is the GET /api/v1/sessions/{id}/transcript body: a
// page of entries, oldest first.
type TranscriptResponse struct {
	ID      string               `json:"id"`
	Backend string               `json:"backend,omitempty"`
	Entries []TranscriptEntryRow `json:"entries"`
	// Before is the cursor of the next older page (pass it as ?before=);
	// present only when HasMore.
	Before  *int64 `json:"before,omitempty"`
	HasMore bool   `json:"hasMore"`
	// Size is the transcript's size in bytes when it was read.
	Size int64 `json:"size"`
}

// maxTranscriptPage mirrors extsessions.MaxPageEntries.
const maxTranscriptPage = 500

// handleSessionTranscript serves a page of a hand-started session's
// transcript.
// GET /api/v1/sessions/{id}/transcript?before=<cursor>&limit=<n>
func (s *Server) handleSessionTranscript(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	id := chi.URLParam(r, "id")
	before := int64(-1)
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErrorWithField(w, http.StatusBadRequest, "bad_request", "before must be a non-negative cursor", "before")
			return
		}
		before = n
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxTranscriptPage {
			writeErrorWithField(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 500", "limit")
			return
		}
		limit = n
	}
	page, err := s.sessions.SessionTranscript(id, before, limit)
	switch {
	case err == nil:
		if page.Entries == nil {
			page.Entries = []TranscriptEntryRow{}
		}
		writeJSON(w, http.StatusOK, page)
	case errors.Is(err, ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no transcript for session "+id)
	default:
		writeError(w, http.StatusInternalServerError, "transcript_unreadable", "the transcript could not be read")
	}
}
