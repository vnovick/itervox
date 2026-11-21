package server

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/logging"
)

// lanes.go — the Lanes view (GET /api/v1/issues/{identifier}/lanes): the
// issue's current branch against its base with every changed file in its
// guardrails.lanes lane, the pattern that put it there, the audit findings
// on each file (with line), why the change needs a person, and the stack
// split record. A viewer read: paths, lanes, patterns, rule ids, lines and
// messages are what the diff's file list and the approval question already
// show. A finding's Text (the added line itself) is a hunk line, so like
// the diff's hunks it needs operator; below admin it is redacted again.

// LanesClient backs the Lanes view. cmd/itervox's changes adapter
// implements it next to ChangesClient (the server asks its ChangesClient
// for it); Lanes runs git and must be called off the event loop.
type LanesClient interface {
	Lanes(ctx context.Context, identifier string) (LanesResponse, error)
}

// LaneFileRow is one changed file in the Lanes view.
type LaneFileRow struct {
	DiffFileRow
	// Lane is "auto", "second_opinion" or "human"; Rule the pattern that
	// put the file there (omitted for auto).
	Lane     string                  `json:"lane"`
	Rule     string                  `json:"rule,omitempty"`
	Findings []guardrails.LaneReason `json:"findings"`
}

// StackSplitRow is an issue's stack split decision (guardrails.lanes.split):
// which pull request goes first and why, and the files on each side.
type StackSplitRow struct {
	// Order is "code_first", "pipeline_first" or "combined" (one PR).
	Order              string    `json:"order"`
	Reason             string    `json:"reason,omitempty"`
	CodeFiles          []string  `json:"codeFiles"`
	CodeFilesTotal     int       `json:"codeFilesTotal"`
	PipelineFiles      []string  `json:"pipelineFiles"`
	PipelineFilesTotal int       `json:"pipelineFilesTotal"`
	CodeLane           string    `json:"codeLane,omitempty"`
	LowerBranch        string    `json:"lowerBranch,omitempty"`
	UpperBranch        string    `json:"upperBranch,omitempty"`
	LowerPR            int       `json:"lowerPr,omitempty"`
	LowerPRURL         string    `json:"lowerPrUrl,omitempty"`
	UpperPRURL         string    `json:"upperPrUrl,omitempty"`
	DecidedAt          time.Time `json:"decidedAt,omitzero"`
}

// LanesResponse is GET /api/v1/issues/{identifier}/lanes.
type LanesResponse struct {
	Identifier string `json:"identifier"`
	Base       string `json:"base"`
	MergeBase  string `json:"mergeBase"`
	// Enforced: the guardrails are on and a lane has patterns.
	Enforced bool `json:"enforced"`
	// Lane is the highest lane of any changed file.
	Lane              string        `json:"lane"`
	Files             []LaneFileRow `json:"files"`
	Truncated         bool          `json:"truncated,omitempty"`
	FindingsTruncated bool          `json:"findingsTruncated,omitempty"`
	// Reasons say why the change needs a person: findings first, then the
	// lane pattern of each file above auto. ReasonsTotal is how many.
	Reasons      []guardrails.LaneReason `json:"reasons"`
	ReasonsTotal int                     `json:"reasonsTotal"`
	// Split is the stack split decision, when one was made.
	Split *StackSplitRow `json:"split,omitempty"`
	// RecordedLane is the lane the issue's pushes reached (merge policy).
	RecordedLane string `json:"recordedLane,omitempty"`
	// ApprovalPending: a push of the issue waits for a person now.
	ApprovalPending bool `json:"approvalPending,omitempty"`
	// TextAllowed tells the client whether findings carry their line's
	// text (operator and above).
	TextAllowed bool `json:"textAllowed"`
}

// laneReasonsForScope is reasons as a caller of scope may see them: a
// finding's line text (a hunk line) withheld below operator and redacted
// again below admin. rs may be shared with other callers, so it copies.
// Every response that carries a guardrails.LaneReason goes through it
// (the Lanes view, the snapshot's input-required rows).
func laneReasonsForScope(rs []guardrails.LaneReason, scope apitokens.Scope) []guardrails.LaneReason {
	if scope == apitokens.ScopeAdmin || rs == nil {
		return rs
	}
	textAllowed := scope.Allows(apitokens.ScopeOperator)
	out := make([]guardrails.LaneReason, len(rs))
	for i, r := range rs {
		if textAllowed {
			r.Text = logging.RedactString(r.Text)
		} else {
			r.Text = ""
		}
		out[i] = r
	}
	return out
}

// lanesForScope withholds a finding's line text below operator and
// redacts it again below admin.
func lanesForScope(resp LanesResponse, scope apitokens.Scope) LanesResponse {
	resp.TextAllowed = scope.Allows(apitokens.ScopeOperator)
	if scope == apitokens.ScopeAdmin {
		return resp
	}
	files := make([]LaneFileRow, len(resp.Files))
	for i, f := range resp.Files {
		f.Findings = laneReasonsForScope(f.Findings, scope)
		files[i] = f
	}
	resp.Files, resp.Reasons = files, laneReasonsForScope(resp.Reasons, scope)
	return resp
}

// handleIssueLanes serves GET /api/v1/issues/{identifier}/lanes.
func (s *Server) handleIssueLanes(w http.ResponseWriter, r *http.Request) {
	lc, ok := s.changes.(LanesClient)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "the lanes view is not available")
		return
	}
	id := chi.URLParam(r, "identifier")
	resp, err := lc.Lanes(r.Context(), id)
	if err != nil {
		writeChangesError(w, err)
		return
	}
	resp.Identifier = id
	if resp.Files == nil {
		resp.Files = []LaneFileRow{}
	}
	if resp.Reasons == nil {
		resp.Reasons = []guardrails.LaneReason{}
	}
	for i := range resp.Files {
		if resp.Files[i].Findings == nil {
			resp.Files[i].Findings = []guardrails.LaneReason{}
		}
	}
	writeJSON(w, http.StatusOK, lanesForScope(resp, callerScope(r)))
}
