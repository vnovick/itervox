package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/logging"
)

// changes.go — #215: the run context (GET /api/v1/issues/{identifier}/context)
// and the worktree diff (GET /api/v1/issues/{identifier}/diff).
//
// Scopes. Both routes are viewer reads. The run context carries only
// redacted, bounded text (the last assistant step, the last tool call's
// target, never tool output). The diff's file list (paths, statuses, line
// counts) is viewer; a file's hunks (?file=) need operator, because an
// agent can commit a secret and a hunk shows it verbatim. Below admin the
// hunks are additionally passed through logging.RedactString (known secret
// shapes, whitespace preserved); admins see them as written.

// CapabilityRunContext is advertised in StateSnapshot.Capabilities when the
// daemon serves the run-context and diff endpoints (#215).
const CapabilityRunContext = "run_context"

// Errors a ChangesClient returns, mapped to HTTP statuses.
var (
	// ErrNoWorktree: the issue has no worktree on disk (404 no_worktree).
	ErrNoWorktree = errors.New("server: issue has no worktree")
	// ErrDiffBaseNotFound: no base ref has a merge base with the worktree's
	// HEAD (404 base_not_found).
	ErrDiffBaseNotFound = errors.New("server: base branch not found")
	// ErrBadDiffBase: the base parameter is not a ref name (400).
	ErrBadDiffBase = errors.New("server: invalid base")
	// ErrDiffFileNotChanged: ?file= is not in the diff (404 file_not_changed).
	ErrDiffFileNotChanged = errors.New("server: file is not changed")
)

// RunToolCall is the run's most recent tool call: its name and target,
// never its output.
type RunToolCall struct {
	Name   string    `json:"name"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// RunSteerRow is one delivered steering message (redacted, bounded).
type RunSteerRow struct {
	Text     string    `json:"text"`
	At       time.Time `json:"at"`
	Delivery string    `json:"delivery,omitempty"`
}

// HandoffRow is one handoff in the issue's worktree.
type HandoffRow struct {
	Title   string `json:"title"`
	Path    string `json:"path"`
	Partial bool   `json:"partial,omitempty"`
}

// RunContextRow is what a ChangesClient knows about an issue's current run
// (Live) or its latest finished one.
type RunContextRow struct {
	RunID             string        `json:"runId"`
	Live              bool          `json:"live"`
	Kind              string        `json:"kind,omitempty"`
	Profile           string        `json:"profile,omitempty"`
	Backend           string        `json:"backend,omitempty"`
	StartedAt         time.Time     `json:"startedAt,omitzero"`
	FinishedAt        time.Time     `json:"finishedAt,omitzero"`
	Outcome           string        `json:"outcome,omitempty"`
	UpdatedAt         time.Time     `json:"updatedAt,omitzero"`
	BaseBranch        string        `json:"baseBranch,omitempty"`
	CurrentStep       string        `json:"currentStep,omitempty"`
	LastToolCall      *RunToolCall  `json:"lastToolCall,omitempty"`
	ToolCalls         int           `json:"toolCalls"`
	TurnCount         int           `json:"turnCount"`
	InputTokens       int           `json:"inputTokens"`
	OutputTokens      int           `json:"outputTokens"`
	TotalTokens       int           `json:"totalTokens"`
	PendingInput      string        `json:"pendingInput,omitempty"`
	PendingApproval   bool          `json:"pendingApproval,omitempty"`
	QueuedSteers      int           `json:"queuedSteers"`
	Steers            []RunSteerRow `json:"steers"`
	GuardrailBlocks   int           `json:"guardrailBlocks"`
	LastGuardrailKind string        `json:"lastGuardrailKind,omitempty"`
}

// DiffFileRow is one changed file.
type DiffFileRow struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

// DiffResponse is GET /api/v1/issues/{identifier}/diff without ?file=.
type DiffResponse struct {
	Identifier string        `json:"identifier"`
	Base       string        `json:"base"`
	MergeBase  string        `json:"mergeBase"`
	Files      []DiffFileRow `json:"files"`
	Truncated  bool          `json:"truncated,omitempty"`
	// HunksAllowed tells the client whether ?file= is open to its scope.
	HunksAllowed bool `json:"hunksAllowed"`
}

// FileDiffResponse is GET /api/v1/issues/{identifier}/diff?file=.
type FileDiffResponse struct {
	Identifier string      `json:"identifier"`
	Base       string      `json:"base"`
	File       DiffFileRow `json:"file"`
	Patch      string      `json:"patch"`
	Truncated  bool        `json:"truncated,omitempty"`
	Bytes      int         `json:"bytes"`
	MaxBytes   int         `json:"maxBytes"`
}

// RunContextResponse is GET /api/v1/issues/{identifier}/context.
type RunContextResponse struct {
	Identifier string         `json:"identifier"`
	Run        *RunContextRow `json:"run"`
	// ChangedFiles are the worktree's changed files (nil when it has none
	// or no worktree); ChangedFilesError says why they are missing.
	ChangedFiles      []DiffFileRow `json:"changedFiles"`
	ChangedFilesError string        `json:"changedFilesError,omitempty"`
	Handoffs          []HandoffRow  `json:"handoffs"`
}

// ChangesClient backs the run-context and diff endpoints (#215).
// cmd/itervox adapts the orchestrator. Diff and FileDiff run git and must
// be called off the event loop (they are: an HTTP handler goroutine).
type ChangesClient interface {
	RunContext(identifier string) (RunContextRow, bool)
	Diff(ctx context.Context, identifier, base string) (DiffResponse, error)
	FileDiff(ctx context.Context, identifier, base, file string) (FileDiffResponse, error)
	Handoffs(identifier string) []HandoffRow
}

// writeChangesError maps a ChangesClient error to its response.
func writeChangesError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoWorktree):
		writeError(w, http.StatusNotFound, "no_worktree", "the issue has no worktree")
	case errors.Is(err, ErrDiffBaseNotFound):
		writeError(w, http.StatusNotFound, "base_not_found", "no base branch to diff against was found")
	case errors.Is(err, ErrBadDiffBase):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_base", "base must be a branch name", "base")
	case errors.Is(err, ErrDiffFileNotChanged):
		writeErrorWithField(w, http.StatusNotFound, "file_not_changed", "the file is not changed against the base", "file")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "diff_timeout", "git did not answer in time")
	default:
		writeError(w, http.StatusInternalServerError, "diff_failed", "the diff could not be computed")
	}
}

// handleIssueContext serves GET /api/v1/issues/{identifier}/context.
func (s *Server) handleIssueContext(w http.ResponseWriter, r *http.Request) {
	if s.changes == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "run context is not available")
		return
	}
	id := chi.URLParam(r, "identifier")
	resp := RunContextResponse{Identifier: id, Handoffs: s.changes.Handoffs(id)}
	if run, ok := s.changes.RunContext(id); ok {
		resp.Run = &run
	}
	diff, err := s.changes.Diff(r.Context(), id, "")
	switch {
	case err == nil:
		resp.ChangedFiles = diff.Files
	case errors.Is(err, ErrNoWorktree):
		if resp.Run == nil && len(resp.Handoffs) == 0 {
			writeError(w, http.StatusNotFound, "no_run_context", "the issue has no run and no worktree")
			return
		}
		resp.ChangedFilesError = "no_worktree"
	case errors.Is(err, ErrDiffBaseNotFound):
		resp.ChangedFilesError = "base_not_found"
	case errors.Is(err, context.DeadlineExceeded):
		resp.ChangedFilesError = "diff_timeout"
	default:
		resp.ChangedFilesError = "diff_failed"
	}
	if resp.Handoffs == nil {
		resp.Handoffs = []HandoffRow{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleIssueDiff serves GET /api/v1/issues/{identifier}/diff[?base=][&file=].
func (s *Server) handleIssueDiff(w http.ResponseWriter, r *http.Request) {
	if s.changes == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "worktree diffs are not available")
		return
	}
	id := chi.URLParam(r, "identifier")
	base := r.URL.Query().Get("base")
	scope := callerScope(r)
	file := r.URL.Query().Get("file")
	if file == "" {
		diff, err := s.changes.Diff(r.Context(), id, base)
		if err != nil {
			writeChangesError(w, err)
			return
		}
		diff.Identifier = id
		diff.HunksAllowed = scope.Allows(apitokens.ScopeOperator)
		if diff.Files == nil {
			diff.Files = []DiffFileRow{}
		}
		writeJSON(w, http.StatusOK, diff)
		return
	}
	if !scope.Allows(apitokens.ScopeOperator) {
		writeInsufficientScope(w, apitokens.ScopeOperator, scope)
		return
	}
	fd, err := s.changes.FileDiff(r.Context(), id, base, file)
	if err != nil {
		writeChangesError(w, err)
		return
	}
	fd.Identifier = id
	if scope != apitokens.ScopeAdmin {
		fd.Patch = logging.RedactString(fd.Patch)
	}
	writeJSON(w, http.StatusOK, fd)
}
