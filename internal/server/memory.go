package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/memory"
)

// MemoryClient backs the Memory page (#202). Memory lives in two files the
// daemon reads again for every run, so the API reads and writes those files
// directly: no cfg field, no reload. Only the daemon's usage counters go
// through the orchestrator's event loop.
type MemoryClient interface {
	// ListMemory reads both memory files and the runs that used each entry.
	ListMemory() MemoryView
	// CreateMemory adds an entry (also the undo of a delete, which sends the
	// deleted entry back).
	CreateMemory(e memory.Entry) (MemoryEntryView, error)
	// UpdateMemory applies a partial update.
	UpdateMemory(id string, p memory.Patch) (MemoryEntryView, error)
	// DeleteMemory removes an entry and returns it.
	DeleteMemory(id string) (MemoryEntryView, error)
	// PinMemory pins or unpins an entry.
	PinMemory(id string, pinned bool) (MemoryEntryView, error)
	// PromoteMemory moves a private entry into the project's shared file and
	// returns the file's git diff.
	PromoteMemory(ctx context.Context, id string) (MemoryPromoteResult, error)
	// ApproveLesson makes a proposed lesson active in the chosen scope,
	// with the reviewer's edited text when given (#203).
	ApproveLesson(id string, opt memory.ApproveOptions) (MemoryEntryView, error)
	// RejectLesson removes a proposed lesson and remembers it so the same
	// lesson is not proposed again (#203).
	RejectLesson(id string) (MemoryEntryView, error)
	// RecallMemory selects the memory a run of profile on an issue titled
	// query would get, with the run-time ranking (memory.Select), within
	// budget tokens; budget 0 is the configured memory budget (#216).
	RecallMemory(query, profile string, budget int) MemoryRecallView
	// PromoteLessonToRule adds an approved lesson as a rule in the chosen
	// rules layer and archives the lesson with a pointer to the rule (#217).
	PromoteLessonToRule(id string, req LessonRuleRequest) (LessonRuleResult, error)
	// AcceptLessonEnforcement accepts the lesson's enforcement suggestion:
	// it creates the "Enforce: <lesson>" issue in the tracker (#217).
	AcceptLessonEnforcement(ctx context.Context, id string) (LessonEnforceResult, error)
	// DismissLessonEnforcement turns the suggestion down (#217).
	DismissLessonEnforcement(id string) (MemoryEntryView, error)
}

// MemoryRecallMaxBudget caps the budget GET /api/v1/memory/recall accepts.
const MemoryRecallMaxBudget = 8000

// memoryRecallMaxQueryBytes caps the recall query.
const memoryRecallMaxQueryBytes = 2000

// MemoryRecallView is GET /api/v1/memory/recall (#216): the entries in
// rank order (pinned first), redacted, and what the budget left out.
type MemoryRecallView struct {
	// Enabled mirrors memory.enabled: off, runs get no memory block, but
	// recall still ranks the files.
	Enabled bool                `json:"enabled"`
	Profile string              `json:"profile"`
	Budget  int                 `json:"budget"`
	Tokens  int                 `json:"tokens"`
	Dropped int                 `json:"dropped"`
	Entries []MemoryRecallEntry `json:"entries"`
	// OverBudget: the pinned entries alone exceed the budget.
	OverBudget bool `json:"overBudget,omitempty"`
	// UnreadableFiles counts memory files that could not be read.
	UnreadableFiles int `json:"unreadableFiles,omitempty"`
}

// MemoryRecallEntry is one recalled entry.
type MemoryRecallEntry struct {
	ID     string       `json:"id"`
	Type   memory.Type  `json:"type"`
	Text   string       `json:"text"`
	Scope  memory.Scope `json:"scope"`
	Tags   []string     `json:"tags,omitempty"`
	Pinned bool         `json:"pinned,omitempty"`
}

// MemoryView is GET /api/v1/memory.
type MemoryView struct {
	// Enabled mirrors memory.enabled: off, runs get no memory block (the
	// files can still be edited).
	Enabled           bool             `json:"enabled"`
	TokenBudget       int              `json:"tokenBudget"`
	PromptShareTokens int              `json:"promptShareTokens"`
	SchemaURL         string           `json:"schemaUrl"`
	Files             []MemoryFileView `json:"files"`
	// Entries are both files' entries, private first, in file order.
	Entries []MemoryEntryView `json:"entries"`
	// Lessons is the lessons configuration the Lessons tab explains
	// (#203).
	Lessons MemoryLessonsView `json:"lessons"`
}

// MemoryLessonsView is memory.lessons as the Lessons tab shows it (#203).
type MemoryLessonsView struct {
	// Enabled: lessons are proposed (memory.enabled and
	// memory.lessons.enabled).
	Enabled bool `json:"enabled"`
	// Drafter is "template" or "profile:<name>": what drafts new lessons.
	Drafter string `json:"drafter"`
	// Scope is the file new proposals go to.
	Scope              memory.Scope `json:"scope"`
	AutoApprovePrivate bool         `json:"autoApprovePrivate"`
	AutoApproveProject bool         `json:"autoApproveProject"`
	MaxPerIssue        int          `json:"maxPerIssue"`
	RateWindowHours    int          `json:"rateWindowHours"`
	StaleAfterRuns     int          `json:"staleAfterRuns"`
	// Rejected is how many rejections are remembered.
	Rejected int `json:"rejected"`
	// EnforceThreshold is memory.lessons.enforce_threshold: how many times
	// a lesson is seen before enforcement is suggested (#217).
	EnforceThreshold int `json:"enforceThreshold"`
}

// MemoryFileView is one memory file.
type MemoryFileView struct {
	Scope    memory.Scope `json:"scope"`
	Path     string       `json:"path"`
	Exists   bool         `json:"exists"`
	Writable bool         `json:"writable"`
	// Error is why the file could not be read; it contributes no entries
	// and is not rewritten until it is fixed.
	Error string `json:"error,omitempty"`
}

// MemoryEntryView is one entry plus the recent runs that used it.
type MemoryEntryView struct {
	memory.Entry
	// Path is the file that holds the entry (for "Open in editor").
	Path string `json:"path"`
	// Runs are the newest runs in history whose prompt carried the entry.
	Runs []MemoryRunRef `json:"runs"`
	// NotHelping flags an approved lesson that is not helping (#203):
	// "unused" (none of the last memory.lessons.stale_after_runs runs used
	// it), "failing" (most runs that used it failed) or "enforced" (its
	// enforcement suggestion was accepted, #217); NotHelpingReason
	// says it in words. Empty otherwise.
	NotHelping       memory.LessonHealth `json:"notHelping,omitempty"`
	NotHelpingReason string              `json:"notHelpingReason,omitempty"`
	// Analytics is, for a lesson, how often runs used it and how those runs
	// ended (#217).
	Analytics *memory.LessonAnalytics `json:"analytics,omitempty"`
}

// MemoryRunRef is a run that used an entry.
type MemoryRunRef struct {
	RunID      string    `json:"runId,omitempty"`
	Identifier string    `json:"identifier"`
	Status     string    `json:"status"`
	FinishedAt time.Time `json:"finishedAt"`
}

// MemoryPromoteResult is POST /api/v1/memory/{id}/promote.
type MemoryPromoteResult struct {
	Entry MemoryEntryView `json:"entry"`
	// Path is the project file the entry moved into.
	Path string `json:"path"`
	// Diff is `git diff` of that file after the move; empty outside a git
	// repository (DiffNote says why).
	Diff     string `json:"diff"`
	DiffNote string `json:"diffNote,omitempty"`
}

// memoryPinRequest is POST /api/v1/memory/{id}/pin; an empty body pins.
type memoryPinRequest struct {
	Pinned *bool `json:"pinned"`
}

func (s *Server) memoryReady(w http.ResponseWriter) bool {
	if s.memory == nil {
		writeError(w, http.StatusServiceUnavailable, "memory_unavailable", "memory is not configured")
		return false
	}
	return true
}

// writeMemoryError maps the memory store's errors to status codes.
func writeMemoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, memory.ErrNotFound):
		writeError(w, http.StatusNotFound, "memory_not_found", err.Error())
	case errors.Is(err, memory.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "memory_too_large", err.Error())
	case errors.Is(err, memory.ErrConflict):
		writeError(w, http.StatusConflict, "memory_conflict", err.Error())
	case errors.Is(err, memory.ErrInvalid), errors.Is(err, memory.ErrScopeOff):
		writeError(w, http.StatusBadRequest, "memory_invalid", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "memory_write_failed", err.Error())
	}
}

func (s *Server) handleListMemory(w http.ResponseWriter, _ *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.memory.ListMemory())
}

// handleRecallMemory is GET /api/v1/memory/recall?q=&profile=&budget=
// (#216): memory search ranked exactly like run-time retrieval.
func (s *Server) handleRecallMemory(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	if query == "" || len(query) > memoryRecallMaxQueryBytes {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "q is required (at most 2000 bytes)", "q")
		return
	}
	budget := 0
	if v := q.Get("budget"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MemoryRecallMaxBudget {
			writeErrorWithField(w, http.StatusBadRequest, "bad_request", "budget must be between 1 and 8000", "budget")
			return
		}
		budget = n
	}
	out := s.memory.RecallMemory(query, strings.TrimSpace(q.Get("profile")), budget)
	if out.Entries == nil {
		out.Entries = []MemoryRecallEntry{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateMemory(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	var e memory.Entry
	if err := decodeJSONBody(w, r, &e); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	out, err := s.memory.CreateMemory(e)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	s.Notify() // a status change moves the nav's lesson count
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleUpdateMemory(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	var p memory.Patch
	if err := decodeJSONBody(w, r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	out, err := s.memory.UpdateMemory(chi.URLParam(r, "id"), p)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	s.Notify() // a status change moves the nav's lesson count
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	out, err := s.memory.DeleteMemory(chi.URLParam(r, "id"))
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	s.Notify() // a status change moves the nav's lesson count
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePinMemory(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	var req memoryPinRequest
	if err := decodeJSONBody(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	pinned := req.Pinned == nil || *req.Pinned
	out, err := s.memory.PinMemory(chi.URLParam(r, "id"), pinned)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePromoteMemory(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	out, err := s.memory.PromoteMemory(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleApproveLesson is POST /api/v1/memory/{id}/approve (#203). The body
// is optional: {"scope": "private"|"project", "text": "edited text"}.
func (s *Server) handleApproveLesson(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	var opt memory.ApproveOptions
	if err := decodeJSONBody(w, r, &opt); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	out, err := s.memory.ApproveLesson(chi.URLParam(r, "id"), opt)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	s.Notify() // the nav's lesson count
	writeJSON(w, http.StatusOK, out)
}

// handleRejectLesson is POST /api/v1/memory/{id}/reject (#203).
func (s *Server) handleRejectLesson(w http.ResponseWriter, r *http.Request) {
	if !s.memoryReady(w) {
		return
	}
	out, err := s.memory.RejectLesson(chi.URLParam(r, "id"))
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	s.Notify() // the nav's lesson count
	writeJSON(w, http.StatusOK, out)
}
