package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vnovick/itervox/internal/issuefilter"
)

// #182 — the combined tracker scope: projects AND assignee AND labels;
// #198 — the tracker filter (groups of conditions) that generalises it.

func (s *Server) scopeUnsupported(w http.ResponseWriter) bool {
	if s.scopeManager == nil {
		writeError(w, http.StatusNotImplemented, "not_supported", "the tracker scope is only available for Linear and GitHub")
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
// projects = every project, "" assignee = anyone, empty labels = none. With
// "filter": [...] (#198) the body is a tracker filter instead — groups of
// conditions, ANDed inside a group, ORed between groups — saved as
// tracker.filter; [] reads every issue.
func (s *Server) handleSetTrackerScope(w http.ResponseWriter, r *http.Request) {
	if s.scopeUnsupported(w) {
		return
	}
	var body struct {
		Projects []string         `json:"projects"`
		Assignee string           `json:"assignee"`
		Labels   []string         `json:"labels"`
		Filter   *json.RawMessage `json:"filter"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "expected JSON with 'projects', 'assignee' and 'labels', or 'filter'")
		return
	}
	var err error
	if body.Filter != nil {
		var filter issuefilter.Filter
		if filter, err = parseFilterBody(*body.Filter); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_filter", err.Error())
			return
		}
		err = s.scopeManager.SetFilter(r.Context(), filter)
	} else {
		err = s.scopeManager.SetScope(r.Context(), TrackerScope{Projects: body.Projects, Assignee: body.Assignee, Labels: body.Labels})
	}
	if err != nil {
		writeScopeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": s.scopeManager.GetScope(), "ok": true})
}

// parseFilterBody reads a filter from JSON with the WORKFLOW.md parser, so
// the API and the file accept exactly the same conditions.
func parseFilterBody(raw json.RawMessage) (issuefilter.Filter, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	f, err := issuefilter.Parse(v, "filter")
	if err != nil {
		return nil, err
	}
	if f == nil {
		f = issuefilter.Filter{}
	}
	return f, f.Validate("filter")
}

func writeScopeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrScopeAssigneeNotFound):
		writeError(w, http.StatusUnprocessableEntity, "assignee_not_found", err.Error())
	case errors.Is(err, ErrFilterInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_filter", err.Error())
	default:
		writeClientError(w, "tracker_scope_not_saved", err)
	}
}

// handleTrackerFilterPreview counts the issues a proposed filter reads
// (#198). Read-only: nothing is saved and the scope does not change.
// Body: {"filter": [...]}.
func (s *Server) handleTrackerFilterPreview(w http.ResponseWriter, r *http.Request) {
	if s.scopeUnsupported(w) {
		return
	}
	var body struct {
		Filter json.RawMessage `json:"filter"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil || body.Filter == nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "expected JSON with 'filter'")
		return
	}
	filter, err := parseFilterBody(body.Filter)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_filter", err.Error())
		return
	}
	p, err := s.scopeManager.PreviewFilter(r.Context(), filter)
	if err != nil {
		if errors.Is(err, ErrScopeAssigneeNotFound) || errors.Is(err, ErrFilterInvalid) {
			writeScopeError(w, err)
			return
		}
		writeClientError(w, "preview_failed", err)
		return
	}
	if p.Identifiers == nil {
		p.Identifiers = []string{}
	}
	writeJSON(w, http.StatusOK, p)
}

// handleTrackerTeams lists the teams a filter group can name (#198).
func (s *Server) handleTrackerTeams(w http.ResponseWriter, r *http.Request) {
	if s.scopeUnsupported(w) {
		return
	}
	teams, err := s.scopeManager.FetchTeams(r.Context())
	if err != nil {
		writeClientError(w, "fetch_failed", err)
		return
	}
	if teams == nil {
		teams = []TrackerTeam{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
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
