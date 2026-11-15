package server

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/rules"
)

// Lessons round 2 (#217) on the Memory page: promote an approved lesson to a
// rule (a rules-layer write, so admin like PUT /api/v1/rules/{layer}), and
// accept or dismiss the enforcement Itervox suggested for a lesson seen
// memory.lessons.enforce_threshold times (operator, like approving a
// lesson). Accepting creates an "Enforce: <lesson>" issue in the tracker;
// nothing commits code.

// LessonRuleRequest is POST /api/v1/memory/{id}/promote-to-rule.
type LessonRuleRequest struct {
	// Layer is the rules layer the rule is added to: "user" or "project".
	Layer string `json:"layer"`
	// Severity is "must" or "should" (default should).
	Severity rules.Severity `json:"severity,omitempty"`
	// Text, when set, is the rule's text instead of the lesson's.
	Text *string `json:"text,omitempty"`
}

// LessonRuleResult is the promotion's result: the lesson, archived with a
// pointer to the rule, and the rule as written.
type LessonRuleResult struct {
	Entry MemoryEntryView `json:"entry"`
	Layer string          `json:"layer"`
	Path  string          `json:"path"`
	Rule  rules.Rule      `json:"rule"`
}

// LessonEnforceResult is POST /api/v1/memory/{id}/enforcement/accept: the
// lesson with its accepted suggestion and the issue it created.
type LessonEnforceResult struct {
	Entry MemoryEntryView    `json:"entry"`
	Issue LessonEnforceIssue `json:"issue"`
}

// LessonEnforceIssue is the "Enforce:" issue an accepted suggestion
// created.
type LessonEnforceIssue struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url,omitempty"`
}

// ErrEnforcementIssue wraps a tracker failure creating the "Enforce:"
// issue.
var ErrEnforcementIssue = errors.New("the enforcement issue could not be created")

// handlePromoteLessonToRule is POST /api/v1/memory/{id}/promote-to-rule.
func (s *Server) handlePromoteLessonToRule(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	var req LessonRuleRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	out, err := s.memory.PromoteLessonToRule(chi.URLParam(r, "id"), req)
	switch {
	case err == nil:
		s.Notify()
		writeJSON(w, http.StatusOK, out)
	case errors.Is(err, ErrUnknownRulesLayer):
		writeError(w, http.StatusNotFound, "unknown_layer", err.Error())
	case errors.Is(err, rules.ErrStale):
		writeError(w, http.StatusConflict, "rules_stale", err.Error())
	case errors.Is(err, rules.ErrInvalid):
		writeError(w, http.StatusBadRequest, "rules_invalid", err.Error())
	default:
		writeMemoryError(w, err)
	}
}

// handleAcceptLessonEnforcement is POST
// /api/v1/memory/{id}/enforcement/accept.
func (s *Server) handleAcceptLessonEnforcement(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	out, err := s.memory.AcceptLessonEnforcement(r.Context(), chi.URLParam(r, "id"))
	switch {
	case err == nil:
		s.Notify()
		writeJSON(w, http.StatusOK, out)
	case errors.Is(err, ErrEnforcementIssue):
		writeError(w, http.StatusBadGateway, "enforcement_issue_failed", err.Error())
	default:
		writeMemoryError(w, err)
	}
}

// handleDismissLessonEnforcement is POST
// /api/v1/memory/{id}/enforcement/dismiss.
func (s *Server) handleDismissLessonEnforcement(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	var ignored struct{}
	if err := decodeJSONBody(w, r, &ignored); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	out, err := s.memory.DismissLessonEnforcement(chi.URLParam(r, "id"))
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
