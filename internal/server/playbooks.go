package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
)

// playbooks.go — #218: task-type playbooks, the decision log, steering
// phrases and the "Generate verify skill" readiness action.
//
//	GET  /api/v1/playbooks                          viewer   the playbooks on disk
//	POST /api/v1/issues/{identifier}/playbook       operator the per-issue override
//	GET  /api/v1/issues/{identifier}/decisions      viewer   every run's decision log (redacted)
//	GET  /api/v1/steering/phrases                   viewer   the steering chips
//	POST /api/v1/repo/readiness/verify-skill-issue  operator creates a tracker issue
//
// None of these travel on the shared SSE frame: the dashboard fetches them
// when a view needs them.

// PlaybookNone is the override that attaches no playbook to an issue.
const PlaybookNone = "none"

// PlaybookRow is one playbook file.
type PlaybookRow struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	Keywords        []string `json:"keywords"`
	RequireEvidence []string `json:"requireEvidence"`
}

// PlaybooksResponse is GET /api/v1/playbooks.
type PlaybooksResponse struct {
	Enabled   bool          `json:"enabled"`
	Playbooks []PlaybookRow `json:"playbooks"`
}

// DecisionRowView is one decision of a run's decision log (redacted,
// bounded).
type DecisionRowView struct {
	Time        string    `json:"time,omitempty"`
	At          time.Time `json:"at,omitzero"`
	Decision    string    `json:"decision"`
	Why         string    `json:"why,omitempty"`
	Alternative string    `json:"alternative,omitempty"`
}

// DecisionRunView is one run's decision log.
type DecisionRunView struct {
	Path         string            `json:"path"`
	RunTimestamp string            `json:"runTimestamp,omitempty"`
	Role         string            `json:"role,omitempty"`
	Decisions    []DecisionRowView `json:"decisions"`
	Truncated    bool              `json:"truncated,omitempty"`
	Skipped      int               `json:"skipped,omitempty"`
}

// DecisionsResponse is GET /api/v1/issues/{identifier}/decisions.
type DecisionsResponse struct {
	Identifier string            `json:"identifier"`
	Runs       []DecisionRunView `json:"runs"`
}

// SteeringPhrasesResponse is GET /api/v1/steering/phrases.
type SteeringPhrasesResponse struct {
	Phrases []config.SteeringPhrase `json:"phrases"`
}

// VerifySkillIssueResult is POST /api/v1/repo/readiness/verify-skill-issue.
type VerifySkillIssueResult struct {
	Identifier string `json:"identifier,omitempty"`
	URL        string `json:"url,omitempty"`
}

// ErrUnknownPlaybook: the override names no playbook on disk (400).
var ErrUnknownPlaybook = errors.New("server: unknown playbook")

// PlaybooksClient backs the playbook and decision-log endpoints (#218).
// cmd/itervox adapts the orchestrator; every method reads files or sends
// an event, never blocking on the event loop.
type PlaybooksClient interface {
	Playbooks() PlaybooksResponse
	// SetIssuePlaybook sets identifier's override: a playbook name,
	// PlaybookNone, or "" for automatic. ErrUnknownPlaybook for a name not
	// on disk, ErrBusy when the event channel is full.
	SetIssuePlaybook(identifier, name string) error
	Decisions(identifier string) []DecisionRunView
	// CreateVerifySkillIssue creates the "write a verification skill"
	// tracker issue.
	CreateVerifySkillIssue(ctx context.Context) (VerifySkillIssueResult, error)
}

func (s *Server) handleListPlaybooks(w http.ResponseWriter, _ *http.Request) {
	if s.playbooks == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "playbooks are not available")
		return
	}
	resp := s.playbooks.Playbooks()
	if resp.Playbooks == nil {
		resp.Playbooks = []PlaybookRow{}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSetIssuePlaybook(w http.ResponseWriter, r *http.Request) {
	if s.playbooks == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "playbooks are not available")
		return
	}
	var body struct {
		Playbook string `json:"playbook"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	id := chi.URLParam(r, "identifier")
	switch err := s.playbooks.SetIssuePlaybook(id, body.Playbook); {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]string{"identifier": id, "playbook": body.Playbook})
	case errors.Is(err, ErrUnknownPlaybook):
		writeErrorWithField(w, http.StatusBadRequest, "unknown_playbook", "no playbook named "+body.Playbook, "playbook")
	default:
		writeBusyOr404(w, err, "not_found", "the override could not be set")
	}
}

func (s *Server) handleIssueDecisions(w http.ResponseWriter, r *http.Request) {
	if s.playbooks == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "decision logs are not available")
		return
	}
	id := chi.URLParam(r, "identifier")
	runs := s.playbooks.Decisions(id)
	if runs == nil {
		runs = []DecisionRunView{}
	}
	writeJSON(w, http.StatusOK, DecisionsResponse{Identifier: id, Runs: runs})
}

func (s *Server) handleSteeringPhrases(w http.ResponseWriter, _ *http.Request) {
	phrases := s.steeringPhrases
	if phrases == nil {
		phrases = []config.SteeringPhrase{}
	}
	writeJSON(w, http.StatusOK, SteeringPhrasesResponse{Phrases: phrases})
}

func (s *Server) handleVerifySkillIssue(w http.ResponseWriter, r *http.Request) {
	if s.playbooks == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "issue creation is not available")
		return
	}
	out, err := s.playbooks.CreateVerifySkillIssue(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "issue_create_failed", "the verify-skill issue could not be created: "+logging.RedactString(err.Error()))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
