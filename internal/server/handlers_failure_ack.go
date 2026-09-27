package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// handleAckFailures acknowledges an issue's worker failures up to a time, so
// the dashboard's attention inbox stops counting them (CORE-175). The ack is
// event-loop mediated: this handler only validates and queues it.
//
// Allowed while the daemon drains: an acknowledgement starts no work.
// POST /api/v1/issues/{identifier}/failures/ack  {"upTo": "<RFC3339>"}
// → 202 {"queued": true}; 400 on a missing/unparsable upTo; 404
// issue_not_found when the issue has no recent worker failure; 503
// orchestrator_busy (Retry-After: 1) when the event queue is full.
func (s *Server) handleAckFailures(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	acker, ok := s.client.(FailureAcker)
	if !ok {
		writeError(w, http.StatusNotImplemented, "not_supported", "failure acknowledgement is not available")
		return
	}
	var body struct {
		UpTo string `json:"upTo"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	upTo, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(body.UpTo))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "upTo must be an RFC 3339 time")
		return
	}
	if err := acker.AckFailures(identifier, upTo); err != nil {
		if errors.Is(err, ErrNoWorkerFailure) {
			writeError(w, http.StatusNotFound, "issue_not_found", "issue "+identifier+" has no recent worker failure")
			return
		}
		writeBusyOr404(w, err, "issue_not_found", "issue "+identifier+": "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}
