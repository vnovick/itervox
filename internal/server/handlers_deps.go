package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// depsAnalyzeRequest is the JSON body for POST /api/v1/deps/analyze.
// Profile is optional; empty falls back to the configured
// agent.deps_analyzer_profile. Mode is optional; absent/empty behaves like
// "auto" (incremental when a usable prior sidecar exists, full otherwise).
type depsAnalyzeRequest struct {
	Profile string `json:"profile"`
	Mode    string `json:"mode"`
}

// depsAnalyzeValidModes are the values handleDepsAnalyzeEnqueue accepts for
// the request body's "mode" field. Kept as local string literals (not
// imported from internal/depsanalysis) because internal/server does not
// depend on internal/depsanalysis — see the package dependency order in
// CLAUDE.md; server only knows the DepsAnalyzer interface, not the
// implementation's mode-resolution constants.
var depsAnalyzeValidModes = map[string]struct{}{
	"":            {}, // absent/empty -> "auto"
	"auto":        {},
	"full":        {},
	"incremental": {},
}

// handleDepsAnalyzeEnqueue starts (or returns) an in-flight analyzer job.
// 202 Accepted on enqueue; 503 when the analyzer is not configured;
// 400 on a body parse failure; 422 when the profile cannot be resolved.
func (s *Server) handleDepsAnalyzeEnqueue(w http.ResponseWriter, r *http.Request) {
	if s.depsAnalyzer == nil {
		writeError(w, http.StatusServiceUnavailable, "deps_analyzer_unavailable",
			"dependency analyzer is not configured for this daemon")
		return
	}
	var body depsAnalyzeRequest
	if r.ContentLength != 0 {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_body", "could not read request body")
			return
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeError(w, http.StatusBadRequest, "bad_body", "invalid JSON body")
				return
			}
		}
	}
	profile := strings.TrimSpace(body.Profile)
	if profile == "" {
		profile = s.depsAnalyzer.DefaultProfile()
	}
	if profile == "" {
		writeError(w, http.StatusUnprocessableEntity, "deps_analyzer_profile_required",
			"no profile supplied and agent.deps_analyzer_profile is unset")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if _, ok := depsAnalyzeValidModes[mode]; !ok {
		writeError(w, http.StatusBadRequest, "invalid_mode",
			fmt.Sprintf("unknown mode %q; expected one of auto, full, incremental", body.Mode))
		return
	}
	if mode == "" {
		// Normalize explicitly rather than relying on the DepsAnalyzer
		// implementation to default an empty mode itself — "auto" is this
		// handler's documented contract regardless of what's behind the
		// interface.
		mode = "auto"
	}
	jobID, queuedAt, err := s.depsAnalyzer.EnqueueAnalysis(profile, mode)
	if err != nil {
		// Surface "unknown profile" / "disabled profile" as 422 so the UI can
		// show the validation error without retrying.
		writeError(w, http.StatusUnprocessableEntity, "deps_analyzer_enqueue_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"jobId":    jobID,
		"profile":  profile,
		"queuedAt": queuedAt,
	})
}

// handleDepsAnalyzeStatus returns the analyzer job by ID.
func (s *Server) handleDepsAnalyzeStatus(w http.ResponseWriter, r *http.Request) {
	if s.depsAnalyzer == nil {
		writeError(w, http.StatusServiceUnavailable, "deps_analyzer_unavailable",
			"dependency analyzer is not configured for this daemon")
		return
	}
	jobID := strings.TrimSpace(chi.URLParam(r, "jobId"))
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "missing_job_id", "jobId path parameter is required")
		return
	}
	row, ok := s.depsAnalyzer.Status(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "deps_analyze_job_not_found", "no analyzer job with that ID")
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// handleDepsAnalyzeCancel stops a running analyzer job.
// 204 on success; 404 when no job with that ID is running; 503 when the
// analyzer is not configured.
func (s *Server) handleDepsAnalyzeCancel(w http.ResponseWriter, r *http.Request) {
	if s.depsAnalyzer == nil {
		writeError(w, http.StatusServiceUnavailable, "deps_analyzer_unavailable",
			"dependency analyzer is not configured for this daemon")
		return
	}
	jobID := strings.TrimSpace(chi.URLParam(r, "jobId"))
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "missing_job_id", "jobId path parameter is required")
		return
	}
	if !s.depsAnalyzer.CancelAnalysis(jobID) {
		writeError(w, http.StatusNotFound, "deps_analyze_job_not_running",
			"no running analyzer job with that ID")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetDepsOverride dismisses the LLM-inferred dependency gating layer
// for the given issue identifier (unified-dependency-graph Task 6). The
// dismissal is routed through the orchestrator's event loop; 202 Accepted
// means the request was queued, not that it has taken effect yet — poll
// GET /api/v1/state (or the SSE stream) to observe the resulting
// InferredDeps entry.
//
// Unlike sibling issue-action handlers (resume, dismiss-input, ...) there is
// no "must already be in state X" precondition to violate, so a false return
// means only one thing: the orchestrator's event channel was full. That is
// surfaced as 503 rather than 404 so the client knows to retry rather than
// treat identifier as unknown.
func (s *Server) handleSetDepsOverride(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if !s.client.SetDepsOverride(identifier, true) {
		writeError(w, http.StatusServiceUnavailable, "deps_override_queue_full",
			"orchestrator event queue is full; retry")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"identifier": identifier, "overridden": true})
}

// handleClearDepsOverride restores the LLM-inferred dependency gating layer
// for the given issue identifier by clearing a prior dismissal. See
// handleSetDepsOverride for the 202-vs-503 semantics.
func (s *Server) handleClearDepsOverride(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if !s.client.SetDepsOverride(identifier, false) {
		writeError(w, http.StatusServiceUnavailable, "deps_override_queue_full",
			"orchestrator event queue is full; retry")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"identifier": identifier, "overridden": false})
}
