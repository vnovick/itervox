package server

import (
	"encoding/json"
	"net/http"
)

// handlers_retries.go houses the retry-budget settings endpoints (gap G).
// Extracted from handlers.go to keep that file under its size-budget cap.

// decodeRequiredField parses a single-field JSON body of the form
// `{"<field>": <value>}` where the field is REQUIRED. Returns the value
// or, on any error (bad JSON, missing field), writes a 400 and returns
// (zero, false). Generic over the value type. Gap §3.5 — DRYs up the
// four settings PUT handlers that all repeated the same parse + nil-check.
func decodeRequiredField[T any](w http.ResponseWriter, r *http.Request, field string) (T, bool) {
	var zero T
	body := make(map[string]json.RawMessage)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return zero, false
	}
	raw, ok := body[field]
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", field+" field is required")
		return zero, false
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid "+field+" value")
		return zero, false
	}
	return value, true
}

// handleSetMaxRetries updates the per-issue retry budget.
// PUT /api/v1/settings/agent/max-retries
// Body: {"maxRetries": 5}  // 0 means unlimited
func (s *Server) handleSetMaxRetries(w http.ResponseWriter, r *http.Request) {
	maxRetries, ok := decodeRequiredField[int](w, r, "maxRetries")
	if !ok {
		return
	}
	if maxRetries < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "maxRetries must be >= 0")
		return
	}
	if err := s.client.SetMaxRetries(maxRetries); err != nil {
		writeError(w, http.StatusInternalServerError, "set_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "maxRetries": maxRetries})
}

// handleSetMaxSwitches updates the per-issue rate_limited switch cap. Gap E.
// PUT /api/v1/settings/agent/max-switches-per-issue-per-window
// Body: {"maxSwitchesPerIssuePerWindow": 2}  // 0 = unlimited
func (s *Server) handleSetMaxSwitches(w http.ResponseWriter, r *http.Request) {
	cap, ok := decodeRequiredField[int](w, r, "maxSwitchesPerIssuePerWindow")
	if !ok {
		return
	}
	if cap < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "maxSwitchesPerIssuePerWindow must be >= 0")
		return
	}
	if err := s.client.SetMaxSwitchesPerIssuePerWindow(cap); err != nil {
		writeError(w, http.StatusInternalServerError, "set_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                           true,
		"maxSwitchesPerIssuePerWindow": cap,
	})
}

// handleSetSwitchWindowHours updates the rolling-window duration. Gap E.
// PUT /api/v1/settings/agent/switch-window-hours
// Body: {"switchWindowHours": 6}  // values <= 0 normalise to 6 server-side
func (s *Server) handleSetSwitchWindowHours(w http.ResponseWriter, r *http.Request) {
	hours, ok := decodeRequiredField[int](w, r, "switchWindowHours")
	if !ok {
		return
	}
	if err := s.client.SetSwitchWindowHours(hours); err != nil {
		writeError(w, http.StatusInternalServerError, "set_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"switchWindowHours": hours,
	})
}

// handleSetFailedState updates the tracker state issues are moved to when
// retries exhaust. Empty string means "pause instead of move".
// PUT /api/v1/settings/tracker/failed-state
// Body: {"failedState": "Backlog"}  // or "" to pause
func (s *Server) handleSetFailedState(w http.ResponseWriter, r *http.Request) {
	failedState, ok := decodeRequiredField[string](w, r, "failedState")
	if !ok {
		return
	}
	if err := s.client.SetFailedState(failedState); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_state", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "failedState": failedState})
}
