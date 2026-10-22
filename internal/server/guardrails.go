package server

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/guardrails"
)

// GuardrailsClient backs the guardrail endpoints a run's shims and pre-push
// hook call (#204). Every route is authenticated by the run's guardrail
// token (an agent-action grant for guardrails.TokenAction scoped to the
// issue and run), never by the dashboard token, and none executes
// anything: a report is recorded, an approval request is raised to a
// human, and its status is read. The command an agent waits to run never
// leaves the agent's machine; the daemon only sees its redacted display
// form.
type GuardrailsClient interface {
	// GuardrailReport records a refused operation.
	GuardrailReport(identifier, runID string, ev guardrails.Event) error
	// RequestGuardrailApproval raises (or joins) an approval request.
	RequestGuardrailApproval(identifier, runID string, req guardrails.ApprovalRequest) (guardrails.ApprovalStatus, error)
	// GuardrailApprovalStatus reads one; an approval is spent by the read
	// that sees it.
	GuardrailApprovalStatus(identifier, runID, id string) guardrails.ApprovalStatus
	// ProvideAgentInput is the agent-actions provide-input path: it never
	// answers a guardrail approval. Same error contract as ProvideInput.
	ProvideAgentInput(identifier, message string) error
}

// InputViaSlack is the only value of provide-input's optional "via" field
// (#230): a human reply the hub's Slack app relays for an allow-listed
// Slack user.
const InputViaSlack = "slack"

// InputViaProvider is implemented by a Client that records which human
// channel a reply arrived through (#230). Same error contract as
// ProvideInput; it is a human reply, so it may answer a guardrail approval.
type InputViaProvider interface {
	ProvideInputVia(identifier, message, via string) error
}

func (s *Server) guardrailGrant(w http.ResponseWriter, r *http.Request) (runID string, ok bool) {
	if s.guardrails == nil {
		writeError(w, http.StatusNotImplemented, "not_supported", "guardrails are not configured")
		return "", false
	}
	grant, ok := s.validateAgentActionRequest(w, r, guardrails.TokenAction)
	if !ok {
		return "", false
	}
	return grant.RunSessionID, true
}

// handleGuardrailEvent records a refused operation.
// POST /api/v1/agent-actions/{identifier}/guardrail/events
func (s *Server) handleGuardrailEvent(w http.ResponseWriter, r *http.Request) {
	runID, ok := s.guardrailGrant(w, r)
	if !ok {
		return
	}
	var ev guardrails.Event
	if err := decodeJSONBody(w, r, &ev); err != nil || !guardrails.IsKind(ev.Kind) {
		writeError(w, http.StatusBadRequest, "bad_request", "kind must be a guardrail kind")
		return
	}
	if err := s.guardrails.GuardrailReport(chi.URLParam(r, "identifier"), runID, ev); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGuardrailApprovalRequest raises an approval request.
// POST /api/v1/agent-actions/{identifier}/guardrail/approvals
func (s *Server) handleGuardrailApprovalRequest(w http.ResponseWriter, r *http.Request) {
	runID, ok := s.guardrailGrant(w, r)
	if !ok {
		return
	}
	var req guardrails.ApprovalRequest
	if err := decodeJSONBody(w, r, &req); err != nil || strings.TrimSpace(req.Key) == "" || strings.TrimSpace(req.Command) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "key and command are required")
		return
	}
	st, err := s.guardrails.RequestGuardrailApproval(chi.URLParam(r, "identifier"), runID, req)
	if err != nil {
		writeBusyOr404(w, err, "not_found", "approvals are not available")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleGuardrailApprovalStatus reads an approval request.
// GET /api/v1/agent-actions/{identifier}/guardrail/approvals/{id}
func (s *Server) handleGuardrailApprovalStatus(w http.ResponseWriter, r *http.Request) {
	runID, ok := s.guardrailGrant(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.guardrails.GuardrailApprovalStatus(chi.URLParam(r, "identifier"), runID, chi.URLParam(r, "id")))
}
