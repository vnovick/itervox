package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// depsAnalyzeRequest is the JSON body for POST /api/v1/deps/analyze.
// Profile is optional; empty falls back to the configured
// agent.deps_analyzer_profile.
type depsAnalyzeRequest struct {
	Profile string `json:"profile"`
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
	jobID, queuedAt, err := s.depsAnalyzer.EnqueueAnalysis(profile)
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
