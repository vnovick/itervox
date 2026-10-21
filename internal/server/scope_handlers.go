package server

import (
	"errors"
	"net/http"
)

// #182 — the combined tracker scope: projects AND assignee AND labels.

func (s *Server) scopeUnsupported(w http.ResponseWriter) bool {
	if s.scopeManager == nil {
		writeError(w, http.StatusNotImplemented, "not_supported", "the combined tracker scope is only available for Linear")
		return true
	}
	return false
}

// handleGetTrackerScope returns the effective tracker scope.
func (s *Server) handleGetTrackerScope(w http.ResponseWriter, _ *http.Request) {
	if s.scopeUnsupported(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": s.scopeManager.GetScope()})
}

// handleSetTrackerScope replaces the tracker scope.
// Body: {"projects": [...], "assignee": "me", "labels": [...]}; empty
// projects = every project, "" assignee = anyone, empty labels = none.
func (s *Server) handleSetTrackerScope(w http.ResponseWriter, r *http.Request) {
	if s.scopeUnsupported(w) {
		return
	}
	var body TrackerScope
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "expected JSON with 'projects', 'assignee' and 'labels'")
		return
	}
	if err := s.scopeManager.SetScope(r.Context(), body); err != nil {
		if errors.Is(err, ErrScopeAssigneeNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "assignee_not_found", err.Error())
			return
		}
		writeClientError(w, "tracker_scope_not_saved", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": s.scopeManager.GetScope(), "ok": true})
}

// handleTrackerLabels lists the labels the scope's label filter can use.
func (s *Server) handleTrackerLabels(w http.ResponseWriter, r *http.Request) {
	if s.scopeUnsupported(w) {
		return
	}
	labels, err := s.scopeManager.FetchLabels(r.Context())
	if err != nil {
		writeClientError(w, "fetch_failed", err)
		return
	}
	if labels == nil {
		labels = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": labels})
}

// handleTrackerUsers lists the users the scope's assignee can be.
func (s *Server) handleTrackerUsers(w http.ResponseWriter, r *http.Request) {
	if s.scopeUnsupported(w) {
		return
	}
	users, err := s.scopeManager.FetchUsers(r.Context())
	if err != nil {
		writeClientError(w, "fetch_failed", err)
		return
	}
	if users == nil {
		users = []TrackerUser{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}
