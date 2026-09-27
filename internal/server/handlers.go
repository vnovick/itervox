package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/automationconfig"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

const sseWriteDeadline = 5 * time.Second

func setSSEWriteDeadline(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline))
}

func writeSSEFrame(w http.ResponseWriter, format string, args ...any) error {
	setSSEWriteDeadline(w)
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func flushSSE(w http.ResponseWriter, flusher http.Flusher) {
	setSSEWriteDeadline(w)
	flusher.Flush()
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap := s.snapshot()
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	select {
	case s.refreshChan <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"queued":    true,
		"queued_at": time.Now(),
	})
}

// handleEvents streams state snapshots as Server-Sent Events.
// Each event is a "data: <JSON>\n\n" frame carrying the full StateSnapshot.
// A named keepalive event is sent after 25 s of stream inactivity to prevent proxy timeouts.
// GET /api/v1/events
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	setSSEHeaders(w)

	flusher, ok := sseFlusher(w)
	if !ok {
		return
	}

	// Send initial snapshot immediately.
	if err := s.writeSSEEvent(w, flusher); err != nil {
		return
	}

	// Subscribe to state-change signals.
	sub := s.bc.subscribe()
	defer s.bc.unsubscribe(sub)

	// Keep-alive ticker (every 25s) to prevent proxy timeouts. Reset on
	// every real event sent so a busy stream (one snapshot per second)
	// doesn't ALSO emit a keepalive every 25s — gap §7.2. The reset
	// halves outbound byte volume on heavy systems while still firing
	// the keepalive within 25s of any quiet period.
	const keepaliveInterval = 25 * time.Second
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsDone: // generation shut down (CORE-025); the client reconnects
			return
		case <-sub:
			if err := s.writeSSEEvent(w, flusher); err != nil {
				return
			}
			ticker.Reset(keepaliveInterval) // §7.2 — defer keepalive after activity
		case <-ticker.C:
			// Send SSE keepalive as a NAMED event (not a comment) so the
			// client's @microsoft/fetch-event-source delivers it to onMessage.
			// Comments (`: ping`) are stripped by the SSE parser per spec —
			// using them meant the dashboard's silence watchdog could not
			// distinguish "no real updates" from "connection dead", so the
			// "Reconnecting…" banner kept appearing on quiet systems. The
			// payload is intentionally tiny; the client checks event type
			// and short-circuits without re-parsing the snapshot.
			if err := writeSSEFrame(w, "event: keepalive\ndata: {}\n\n"); err != nil {
				return
			}
			flushSSE(w, flusher)
		}
	}
}

func (s *Server) writeSSEEvent(w http.ResponseWriter, flusher http.Flusher) error {
	snap := s.snapshot()
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if err := writeSSEFrame(w, "data: %s\n\n", b); err != nil {
		return err
	}
	flushSSE(w, flusher)
	return nil
}

// writeBusyOr404 maps an issue-control error to its HTTP response: ErrBusy
// becomes 503 with a Retry-After hint (the orchestrator's event channel was
// full — nothing was enqueued, so a single client retry is safe), and any
// other non-nil error becomes 404 with the caller-supplied code/message
// (the method's own synchronous lookup established the issue is not in the
// required state). Callers pass err == nil themselves; this is only invoked
// on the error path (CORE-005).
// issueError prefixes an issue-control failure with the issue it concerns
// (BH-M5-5). The dashboard toast store dedupes by message, so a message that
// does not name the issue merges failures for different issues into one toast.
// The chain is kept for errors.Is, and an error that already names the issue
// is returned unchanged so the identifier never appears twice.
func issueError(identifier string, err error) error {
	if err == nil || identifier == "" || strings.Contains(err.Error(), identifier) {
		return err
	}
	return fmt.Errorf("issue %s: %w", identifier, err)
}

func writeBusyOr404(w http.ResponseWriter, err error, notFoundCode, notFoundMsg string) {
	if writeDrainingConflict(w, err) {
		return
	}
	if errors.Is(err, ErrBusy) {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "orchestrator_busy", "orchestrator event queue is full; retry")
		return
	}
	writeError(w, http.StatusNotFound, notFoundCode, notFoundMsg)
}

// handleReanalyzeIssue moves a paused issue to the forced re-analysis queue,
// bypassing the open-PR guard on next dispatch.
// POST /api/v1/issues/{identifier}/reanalyze
func (s *Server) handleReanalyzeIssue(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if err := s.client.ReanalyzeIssue(identifier); err != nil {
		writeBusyOr404(w, err, "not_paused", "issue "+identifier+" is not paused")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": true, "identifier": identifier})
}

// handleResumeIssue removes a paused issue from the pause set so it can be dispatched again.
func (s *Server) handleResumeIssue(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if err := s.client.ResumeIssue(identifier); err != nil {
		writeBusyOr404(w, err, "not_paused", "issue "+identifier+" is not paused")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resumed": true, "identifier": identifier})
}

// handleCancelIssue cancels the running worker for the given issue identifier.
func (s *Server) handleCancelIssue(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if err := s.client.CancelIssue(identifier); err != nil {
		writeBusyOr404(w, err, "not_running", "issue "+identifier+" is not running")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true, "identifier": identifier})
}

// handleTerminateIssue hard-stops a running or paused issue without adding it to PausedIdentifiers.
func (s *Server) handleTerminateIssue(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if err := s.client.TerminateIssue(identifier); err != nil {
		writeBusyOr404(w, err, "not_found", "issue "+identifier+" is not running or paused")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminated": true, "identifier": identifier})
}

// handleIssueDetail returns a single issue by identifier, enriched with orchestrator state.
func (s *Server) handleIssueDetail(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")

	// Fast path: use the single-item callback when available.
	if s.fetchIssue != nil {
		issue, err := s.fetchIssue(r.Context(), identifier)
		if err != nil {
			writeClientError(w, "fetch_failed", err)
			return
		}
		if issue == nil {
			writeError(w, http.StatusNotFound, "not_found", "issue "+identifier+" not found")
			return
		}
		writeJSON(w, http.StatusOK, *issue)
		return
	}

	// Slow path: scan all issues.
	issues, err := s.client.FetchIssues(r.Context())
	if err != nil {
		writeClientError(w, "fetch_failed", err)
		return
	}
	for _, issue := range issues {
		if issue.Identifier == identifier {
			writeJSON(w, http.StatusOK, issue)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "issue "+identifier+" not found")
}

// handleIssues returns all project issues enriched with orchestrator state.
func (s *Server) handleIssues(w http.ResponseWriter, r *http.Request) {
	issues, err := s.client.FetchIssues(r.Context())
	if err != nil {
		writeClientError(w, "fetch_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, issues)
}

// handleLogs streams the itervox log file as Server-Sent Events.
// On connect it sends the last 16 KB of the file, then tails for new lines.
// Each SSE event is: event: log\ndata: <one log line>\n\n
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.logFile == "" {
		http.Error(w, "log file not configured", http.StatusNotFound)
		return
	}
	flusher, ok := beginSSE(w)
	if !ok {
		return
	}

	f, err := os.Open(s.logFile)
	if err != nil {
		_ = writeSSEFrame(w, "event: log\ndata: [log file not yet available: %s]\n\n", err)
		flushSSE(w, flusher)
		return
	}
	defer func() { _ = f.Close() }()

	// Seek to last 16 KB for initial history.
	const tail = 16 * 1024
	if fi, err := f.Stat(); err == nil && fi.Size() > tail {
		_, _ = f.Seek(-tail, io.SeekEnd)
		// Skip to next newline so we don't send a partial line. T-51: read a
		// small chunk at a time instead of byte-by-byte so we don't issue 1
		// syscall per byte for the (potentially long) leading partial line.
		var skipBuf [256]byte
		for {
			n, err := f.Read(skipBuf[:])
			if err != nil || n == 0 {
				break
			}
			if idx := bytes.IndexByte(skipBuf[:n], '\n'); idx >= 0 {
				// Rewind so the next read starts AFTER the newline (not
				// somewhere mid-following-line).
				_, _ = f.Seek(int64(idx+1-n), io.SeekCurrent)
				break
			}
		}
	}

	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	// maxPending caps the incomplete-line carry buffer so a runaway log stream
	// without newlines cannot grow pending unboundedly.
	const maxPending = 256 * 1024 // 256 KB

	readBuf := make([]byte, 32*1024)
	var pending bytes.Buffer

	// Optional identifier filter: only emit lines belonging to this issue.
	// We parse each line as JSON and match on the issue_identifier field for
	// exactness — a substring match on raw bytes can produce false positives
	// (e.g. "PROJ-1" matches "PROJ-10"). Fall back to substring for non-JSON
	// lines so that legacy plain-text entries are still included (GO-R10-6).
	filterID := r.URL.Query().Get("identifier")

	lineMatchesFilter := func(line string) bool {
		if filterID == "" {
			return true
		}
		var entry struct {
			IssueIdentifier string `json:"issue_identifier"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			return entry.IssueIdentifier == filterID
		}
		// Non-JSON fallback: substring match.
		return strings.Contains(line, filterID)
	}

	flushPending := func() bool {
		for {
			idx := bytes.IndexByte(pending.Bytes(), '\n')
			if idx < 0 {
				break
			}
			line := string(pending.Next(idx + 1))
			line = strings.TrimRight(line, "\n")
			if line == "" {
				continue
			}
			if !lineMatchesFilter(line) {
				continue
			}
			if err := writeSSEFrame(w, "event: log\ndata: %s\n\n", line); err != nil {
				return false
			}
		}
		return true
	}

	flush := func() bool {
		for {
			n, err := f.Read(readBuf)
			if n > 0 {
				if pending.Len()+n > maxPending {
					// Flush what we have before accepting more so data is not dropped.
					if !flushPending() {
						return false
					}
				}
				pending.Write(readBuf[:n])
			}
			// Send complete lines.
			if !flushPending() {
				return false
			}
			if err != nil || n == 0 {
				// n == 0 with err == nil means no new data (EOF on regular file);
				// break to avoid a busy-spin until the next ticker tick (GO-R10-5).
				break
			}
		}
		flushSSE(w, flusher)
		return true
	}

	if !flush() { // send initial tail immediately
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsDone: // generation shut down (CORE-025)
			return
		case <-ticker.C:
			if !flush() {
				return
			}
		}
	}
}

// handleIssueLogs returns parsed log entries for a specific issue identifier
// from the in-memory log buffer (only available for currently-running sessions).
func (s *Server) handleIssueLogs(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	lines := s.client.FetchLogs(r.Context(), identifier)
	entries := make([]IssueLogEntry, 0, len(lines))
	for _, line := range lines {
		entry, skip := parseLogLine(line)
		if skip {
			continue
		}
		entries = append(entries, entry)
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleClearIssueLogs deletes the in-memory and on-disk log buffer for an issue.
// DELETE /api/v1/issues/{identifier}/logs
func (s *Server) handleClearIssueLogs(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if err := s.client.ClearLogs(identifier); err != nil {
		writeClientError(w, "clear_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleClearIssueSubLogs deletes all JSONL session files for one issue.
// DELETE /api/v1/issues/{identifier}/sublogs
func (s *Server) handleClearIssueSubLogs(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if err := s.client.ClearIssueSubLogs(identifier); err != nil {
		writeClientError(w, "clear_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleLogIdentifiers returns a list of issue identifiers that have log data
// (either in-memory or on-disk). Used by the Logs page sidebar to show only
// issues with actual log files, not all tracker issues.
// GET /api/v1/logs/identifiers
func (s *Server) handleLogIdentifiers(w http.ResponseWriter, r *http.Request) {
	ids := s.client.FetchLogIdentifiers()
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, ids)
}

// handleClearAllLogs deletes in-memory and on-disk log buffers for all issues.
// DELETE /api/v1/logs
func (s *Server) handleClearAllLogs(w http.ResponseWriter, r *http.Request) {
	if err := s.client.ClearAllLogs(); err != nil {
		writeClientError(w, "clear_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleClearSessionSublog deletes the JSONL file for a specific agent session run.
// DELETE /api/v1/issues/{identifier}/sublogs/{sessionId}
func (s *Server) handleClearSessionSublog(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	sessionID := chi.URLParam(r, "sessionId")
	if err := s.client.ClearSessionSublog(identifier, sessionID); err != nil {
		writeClientError(w, "clear_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSubLogs returns parsed session log entries from CLAUDE_CODE_LOG_DIR files.
// This endpoint reads .jsonl stream-json files written by Claude Code when
// CLAUDE_CODE_LOG_DIR is set, covering all subagents spawned during the session.
// Returns an empty array when no logs exist (not an error).
// GET /api/v1/issues/{identifier}/sublogs
func (s *Server) handleSubLogs(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	entries, err := s.client.FetchSubLogs(r.Context(), identifier)
	if err != nil {
		writeClientError(w, "fetch_failed", err)
		return
	}
	if entries == nil {
		entries = []domain.IssueLogEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleAIReview dispatches a reviewer worker for the given issue identifier.
// POST /api/v1/issues/{identifier}/ai-review
func (s *Server) handleAIReview(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if err := s.client.DispatchReviewer(identifier); err != nil {
		if writeDrainingConflict(w, err) {
			return
		}
		writeClientError(w, "dispatch_failed", issueError(identifier, err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"queued":     true,
		"identifier": identifier,
	})
}

// handleListProjects returns all projects visible to the API key.
// Only available when a ProjectManager (Linear) is configured; returns 501 otherwise.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	if s.projectManager == nil {
		writeError(w, http.StatusNotImplemented, "not_supported", "project listing is only available for Linear")
		return
	}
	projects, err := s.projectManager.FetchProjects(r.Context())
	if err != nil {
		writeClientError(w, "fetch_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

// handleGetProjectFilter returns the current runtime project filter.
func (s *Server) handleGetProjectFilter(w http.ResponseWriter, r *http.Request) {
	if s.projectManager == nil {
		writeError(w, http.StatusNotImplemented, "not_supported", "project filter is only available for Linear")
		return
	}
	slugs := s.projectManager.GetProjectFilter()
	writeJSON(w, http.StatusOK, map[string]any{"filter": slugs})
}

// handleSetProjectFilter replaces the runtime project filter.
// Body: {"slugs": ["<slug>", ...]}  — empty array = all issues, omit/null = reset to WORKFLOW.md default.
func (s *Server) handleSetProjectFilter(w http.ResponseWriter, r *http.Request) {
	if s.projectManager == nil {
		writeError(w, http.StatusNotImplemented, "not_supported", "project filter is only available for Linear")
		return
	}
	var body struct {
		Slugs *[]string `json:"slugs"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "expected JSON with optional 'slugs' array")
		return
	}
	var slugs []string // nil = reset to WORKFLOW.md default
	if body.Slugs != nil {
		slugs = *body.Slugs
	}
	// M4-close D4/D5: a fence refusal is a retryable 503 settings_reloading,
	// a failed persist a 500 — never a 200 for a save that did not happen.
	if err := s.projectManager.SetProjectFilter(slugs); err != nil {
		writeClientError(w, "project_filter_not_saved", err)
		return
	}
	filter := s.projectManager.GetProjectFilter()
	writeJSON(w, http.StatusOK, map[string]any{"filter": filter, "ok": true})
}

// handleUpdateIssueState transitions an issue to a new state in the upstream tracker.
func (s *Server) handleUpdateIssueState(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	var body struct {
		State string `json:"state"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil || body.State == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "state field required")
		return
	}
	ctx := WithIssueStatusSource(r.Context(), IssueStatusSourceDashboard)
	if err := s.client.UpdateIssueState(ctx, identifier, body.State); err != nil {
		writeClientError(w, "update_failed", issueError(identifier, err))
		return
	}
	// Trigger an immediate re-poll so the orchestrator picks up the new state
	// without waiting for the next polling_interval_ms tick.
	select {
	case s.refreshChan <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "identifier": identifier, "state": body.State})
}

// handleSetIssueProfile sets (or clears) the per-issue agent profile override.
// POST /api/v1/issues/{identifier}/profile
// Body: {"profile": "fast"} to set; {"profile": ""} to reset to default.
func (s *Server) handleSetIssueProfile(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	var body struct {
		Profile string `json:"profile"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	s.client.SetIssueProfile(identifier, body.Profile) // empty string = reset to default
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "identifier": identifier, "profile": body.Profile})
}

// handleSetIssueBackend sets (or clears) the per-issue backend override.
// POST /api/v1/issues/{identifier}/backend
// Body: {"backend": "codex"} to set; {"backend": ""} to reset to default.
func (s *Server) handleSetIssueBackend(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	var body struct {
		Backend string `json:"backend"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	// CORE-040: a closed enum, compared exactly. "" clears the override. The
	// value becomes a backend hint that dispatch splits at the first
	// whitespace, so anything else ("claude printf …", " codex") could smuggle
	// a command into the runner's shell. Deliberately no TrimSpace.
	if body.Backend != "" && !config.IsSupportedBackend(body.Backend) {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request",
			fmt.Sprintf("backend must be \"claude\", \"codex\", or \"\" to clear, got %q", body.Backend), "backend")
		return
	}
	// CORE-056: a pin the CORE-115 resolver would refuse is rejected here,
	// with the resolver's reason, rather than stored as an inert pin.
	if checker, ok := s.client.(IssueBackendPinChecker); ok && body.Backend != "" {
		if err := checker.CheckIssueBackendPin(identifier, body.Backend); err != nil {
			writeErrorWithField(w, http.StatusConflict, "backend_pin_refused", issueError(identifier, err).Error(), "backend")
			return
		}
	}
	s.client.SetIssueBackend(identifier, body.Backend)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "identifier": identifier, "backend": body.Backend})
}

func (s *Server) handleProvideInput(w http.ResponseWriter, r *http.Request) {
	// agent.inline_input makes the tracker the only HUMAN reply channel: the
	// operator answers by commenting on the issue, and the dashboard reply box
	// is hidden. The token-gated agent-actions route (handleAgentProvideInput)
	// is deliberately NOT gated — that is an automation policy, not a human
	// channel, and gating it would silently break auto-resume automations.
	if s.snapshot().InlineInput {
		writeError(w, http.StatusConflict, "inline_input_enabled",
			"agent.inline_input is on: reply by commenting on the issue in the tracker")
		return
	}
	identifier := chi.URLParam(r, "identifier")
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if body.Message == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "message is required")
		return
	}
	// ProvideInput performs no lookup of its own (CORE-005): the only error
	// it can return is ErrBusy (event channel full). There is no "not found"
	// branch here any more — whether the issue is actually waiting for input
	// is the event loop's decision, so success means "queued", not "applied".
	if err := s.client.ProvideInput(identifier, body.Message); err != nil {
		writeBusyOr404(w, err, "not_found", "issue "+identifier+" is not in input-required state")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "identifier": identifier})
}

// maxOperatorCommentBytes bounds a dashboard comment. 10 KiB is far above
// any real review note and keeps a pasted log from becoming a tracker
// comment nobody can read.
const maxOperatorCommentBytes = 10 * 1024

// handleIssueComment posts a plain operator comment on an issue.
// POST /api/v1/issues/{identifier}/comment  {"body":"..."}
// 202 {"queued":true}  — accepted by the write-ahead outbox; delivered by the flusher
// 200 {"ok":true}      — posted directly (tracker.outbox: false)
func (s *Server) handleIssueComment(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	var body struct {
		Body string `json:"body"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	text := strings.TrimSpace(body.Body)
	if text == "" {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "body is required", "body")
		return
	}
	if len(text) > maxOperatorCommentBytes {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "body exceeds 10 KiB", "body")
		return
	}
	queued, err := s.client.PostOperatorComment(r.Context(), identifier, text)
	if err != nil {
		if errors.Is(err, tracker.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "issue "+identifier+" not found")
			return
		}
		writeClientError(w, "comment_failed", issueError(identifier, err))
		return
	}
	if queued {
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "identifier": identifier})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "identifier": identifier})
}

func (s *Server) handleDismissInput(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	// Same contract as handleProvideInput: no lookup, so no "not found"
	// branch — only ErrBusy (event channel full) can fail this (CORE-005).
	if err := s.client.DismissInput(identifier); err != nil {
		writeBusyOr404(w, err, "not_found", "issue "+identifier+" is not in input-required state")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "identifier": identifier})
}

func (s *Server) validateAgentActionRequest(w http.ResponseWriter, r *http.Request, action string) (agentactions.Grant, bool) {
	if s.actionTokens == nil {
		writeError(w, http.StatusNotImplemented, "not_supported", "agent actions are not configured")
		return agentactions.Grant{}, false
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return agentactions.Grant{}, false
	}
	token := strings.TrimPrefix(auth, prefix)
	identifier := chi.URLParam(r, "identifier")
	grant, reason, ok := s.actionTokens.Validate(token, identifier, action, time.Now())
	if !ok {
		status := http.StatusForbidden
		if reason == "missing_token" || reason == "unknown_token" || reason == "expired_token" {
			status = http.StatusUnauthorized
		}
		writeError(w, status, "agent_action_denied", reason)
		return agentactions.Grant{}, false
	}
	return grant, true
}

func (s *Server) handleAgentComment(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.validateAgentActionRequest(w, r, config.AgentActionComment); !ok {
		return
	}
	identifier := chi.URLParam(r, "identifier")
	var body struct {
		Body string `json:"body"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil || strings.TrimSpace(body.Body) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "body field required")
		return
	}
	// Mark agent comments as managed so they don't trigger tracker_comment_added
	// automations — preventing unbounded comment-chain loops between agents.
	// The comment is still visible in {{ issue.comments }} for other agents and
	// humans to read. v1 will add a separate agent_comment action with structured
	// metadata for intentional multi-agent communication.
	if err := s.client.CommentOnIssue(r.Context(), identifier, tracker.MarkManagedComment(body.Body)); err != nil {
		writeClientError(w, "comment_failed", err)
		return
	}
	// T-6: track per-issue comment counts so the dashboard can surface a
	// "📝 N reviews" badge on the issue card without re-querying the tracker.
	s.client.BumpCommentCount(identifier)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAgentCreateIssue(w http.ResponseWriter, r *http.Request) {
	grant, ok := s.validateAgentActionRequest(w, r, config.AgentActionCreateIssue)
	if !ok {
		return
	}
	identifier := chi.URLParam(r, "identifier")
	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil || strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "title field required")
		return
	}
	if strings.TrimSpace(grant.CreateIssueState) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "create issue state is not configured for this profile")
		return
	}
	issue, err := s.client.CreateIssue(r.Context(), identifier, body.Title, body.Body, grant.CreateIssueState)
	if err != nil {
		writeClientError(w, "create_issue_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "issue": issue})
}

func (s *Server) handleAgentMoveState(w http.ResponseWriter, r *http.Request) {
	grant, ok := s.validateAgentActionRequest(w, r, config.AgentActionMoveState)
	if !ok {
		return
	}
	identifier := chi.URLParam(r, "identifier")
	var body struct {
		State string `json:"state"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil || strings.TrimSpace(body.State) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "state field required")
		return
	}
	targetState := strings.TrimSpace(body.State)
	if strings.TrimSpace(grant.MoveIssueState) != "" && targetState != strings.TrimSpace(grant.MoveIssueState) {
		writeError(w, http.StatusForbidden, "agent_action_denied", "move_state target is not allowed by this action grant")
		return
	}
	ctx := WithIssueStatusSource(r.Context(), IssueStatusSourceAgent)
	if err := s.client.UpdateIssueState(ctx, identifier, targetState); err != nil {
		writeClientError(w, "update_failed", err)
		return
	}
	select {
	case s.refreshChan <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAgentProvideInput(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.validateAgentActionRequest(w, r, config.AgentActionProvideInput); !ok {
		return
	}
	identifier := chi.URLParam(r, "identifier")
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil || strings.TrimSpace(body.Message) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "message field required")
		return
	}
	// Same ErrBusy-only contract as handleProvideInput. This route is
	// intentionally NOT gated on agent.inline_input (see handleProvideInput's
	// comment) — that policy difference is preserved; only the busy/not-found
	// mapping changes (CORE-005).
	if err := s.client.ProvideInput(identifier, body.Message); err != nil {
		writeBusyOr404(w, err, "not_found", "issue "+identifier+" is not in input-required state")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "identifier": identifier})
}

func (s *Server) handleSetInlineInput(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if err := s.client.SetInlineInput(body.Enabled); err != nil {
		writeClientError(w, "server_error", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSetWorkers updates the max concurrent agents at runtime.
// POST /api/v1/settings/workers
// Body: {"workers": 5} for absolute, {"delta": 1} or {"delta": -1} for relative.
func (s *Server) handleSetWorkers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Workers int `json:"workers"`
		Delta   int `json:"delta"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	var target int
	if body.Workers > 0 {
		// Absolute set: clamp and apply directly.
		target = max(1, min(body.Workers, 50))
		if err := s.client.SetWorkers(target); err != nil {
			writeClientError(w, "persist_failed", err)
			return
		}
	} else {
		// Relative delta: use BumpMaxWorkers for an atomic read-modify-write.
		var err error
		target, err = s.client.BumpWorkers(body.Delta)
		if err != nil {
			writeClientError(w, "persist_failed", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": target})
}

// handleListProfiles returns the current profile definitions.
// GET /api/v1/settings/profiles
func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	defs := s.client.ProfileDefs()
	writeJSON(w, http.StatusOK, map[string]any{"profiles": defs})
}

// handleGetReviewer returns the reviewer configuration.
// GET /api/v1/settings/reviewer
func (s *Server) handleGetReviewer(w http.ResponseWriter, _ *http.Request) {
	profile, autoReview := s.client.ReviewerConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"profile":     profile,
		"auto_review": autoReview,
	})
}

// handleSetReviewer updates the reviewer configuration.
// PUT /api/v1/settings/reviewer
// Body: {"profile": "reviewer", "auto_review": true}
func (s *Server) handleSetReviewer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Profile    string `json:"profile"`
		AutoReview bool   `json:"auto_review"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		// T-50: typed-error parity with the rest of the settings handlers so
		// SettingsError-aware clients (web/src/auth/SettingsError.ts) receive
		// {error:{code,message}} instead of a raw text body.
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON")
		return
	}
	if err := s.client.SetReviewerConfig(body.Profile, body.AutoReview); err != nil {
		if errors.Is(err, config.ErrAutoClearAutoReviewConflict) || errors.Is(err, config.ErrAutoReviewRequiresReviewerProfile) {
			writeError(w, http.StatusBadRequest, "invalid_combination", err.Error())
			return
		}
		if errors.Is(err, config.ErrReviewerProfileNotFound) || errors.Is(err, config.ErrReviewerProfileDisabled) {
			writeError(w, http.StatusBadRequest, "invalid_profile", err.Error())
			return
		}
		writeClientError(w, "persist_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleListModels returns available models from the WORKFLOW.md config.
// GET /api/v1/settings/models
func (s *Server) handleListModels(w http.ResponseWriter, _ *http.Request) {
	models := s.client.AvailableModels()
	if models == nil {
		models = make(map[string][]ModelOption)
	}
	writeJSON(w, http.StatusOK, models)
}

// handleRefreshModels queries Anthropic / OpenAI APIs for the live model
// catalog and rewrites agent.available_models in WORKFLOW.md. The dashboard
// model picker will pick up the change on the next snapshot refresh.
// POST /api/v1/settings/models/refresh
// Body: {"backend": "claude" | "codex" | "all"}  (default: "all")
func (s *Server) handleRefreshModels(w http.ResponseWriter, r *http.Request) {
	if refresher, ok := s.client.(ModelRefresher); ok {
		var body struct {
			Backend string `json:"backend"`
		}
		_ = decodeJSONBody(w, r, &body) // tolerate empty body → "all"
		if body.Backend == "" {
			body.Backend = "all"
		}
		out, err := refresher.RefreshAvailableModels(r.Context(), body.Backend)
		if err != nil {
			writeClientError(w, "refresh_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"models": out,
		})
		select {
		case s.refreshChan <- struct{}{}:
		default:
		}
		return
	}
	writeError(w, http.StatusNotImplemented, "not_implemented",
		"this build does not support models/refresh — use the `itervox models refresh` CLI subcommand instead")
}

// ModelRefresher is an optional capability the daemon's OrchestratorClient
// can implement to power POST /api/v1/settings/models/refresh. Implemented
// by orchestratorAdapter in cmd/itervox; non-orchestrator backends (tests,
// quickstart) can leave it unimplemented and the route returns 501.
type ModelRefresher interface {
	RefreshAvailableModels(ctx context.Context, backend string) (map[string][]ModelOption, error)
}

// handleUpsertProfile creates or updates a named agent profile.
// PUT /api/v1/settings/profiles/{name}
// Body: {"command": "claude --model ...", "soul": "...", "instructions": "...", "backend": "codex"}
func (s *Server) handleUpsertProfile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Command          string   `json:"command"`
		Prompt           string   `json:"prompt"`
		Soul             *string  `json:"soul"`
		Instructions     *string  `json:"instructions"`
		SoulFile         string   `json:"soulFile"`
		InstructionsFile string   `json:"instructionsFile"`
		Backend          string   `json:"backend"`
		Enabled          *bool    `json:"enabled"`
		AllowedActions   []string `json:"allowedActions"`
		CreateIssueState string   `json:"createIssueState"`
		OriginalName     string   `json:"originalName"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil || body.Command == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "command field required")
		return
	}
	if invalid := config.InvalidAgentActions(body.AllowedActions); len(invalid) > 0 {
		writeError(w, http.StatusBadRequest, "invalid_allowed_actions", fmt.Sprintf("unknown allowedActions: %s", strings.Join(invalid, ", ")))
		return
	}
	if slices.Contains(config.NormalizeAllowedActions(body.AllowedActions), config.AgentActionCreateIssue) &&
		strings.TrimSpace(body.CreateIssueState) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "createIssueState is required when create_issue is enabled")
		return
	}
	def := ProfileDef{
		Command:          body.Command,
		Prompt:           body.Prompt,
		SoulFile:         body.SoulFile,
		InstructionsFile: body.InstructionsFile,
		Backend:          body.Backend,
		Enabled:          body.Enabled == nil || *body.Enabled,
		AllowedActions:   config.NormalizeAllowedActions(body.AllowedActions),
		CreateIssueState: strings.TrimSpace(body.CreateIssueState),
	}
	if body.Soul != nil {
		def.Soul = *body.Soul
		def.SoulSet = true
	}
	if body.Instructions != nil {
		def.Instructions = *body.Instructions
		def.InstructionsSet = true
	}
	if err := s.client.UpsertProfile(name, def, strings.TrimSpace(body.OriginalName)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			writeError(w, http.StatusConflict, "profile_exists", err.Error())
			return
		}
		writeClientError(w, "upsert_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteProfile removes a named agent profile.
// DELETE /api/v1/settings/profiles/{name}
func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.client.DeleteProfile(name); err != nil {
		writeClientError(w, "delete_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetAutomations(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Automations []AutomationDef `json:"automations"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	profileDefs := s.client.ProfileDefs()
	profiles := make(map[string]config.AgentProfile, len(profileDefs))
	for name, def := range profileDefs {
		enabled := def.Enabled
		profiles[name] = config.AgentProfile{
			Command:          def.Command,
			Prompt:           def.Prompt,
			Backend:          def.Backend,
			Enabled:          &enabled,
			AllowedActions:   config.NormalizeAllowedActions(def.AllowedActions),
			CreateIssueState: strings.TrimSpace(def.CreateIssueState),
		}
	}
	// CORE-010: pass agent.command so a switch profile with an empty command
	// is validated against the command it inherits at dispatch; a mismatch
	// is a 400 client error, rejected before SetAutomations persists.
	if err := config.ValidateAutomationsWithDefaults(automationconfig.ConfigsFromDefinitions(body.Automations), profiles, s.client.DefaultAgentCommand()); err != nil {
		writeAutomationValidationError(w, err)
		return
	}
	if err := s.client.SetAutomations(body.Automations); err != nil {
		writeClientError(w, "set_automations_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTestAutomation dispatches a one-off worker for the named automation
// rule against the given issue identifier (T-10). The resulting run is tagged
// TriggerType="test" so the timeline / activity surfaces can distinguish it
// from production fires while still treating it as automation activity for
// the "automation runs only" chips. Cron rules can be test-fired outside
// their normal schedule.
func (s *Server) handleTestAutomation(w http.ResponseWriter, r *http.Request) {
	automationID := chi.URLParam(r, "id")
	if strings.TrimSpace(automationID) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "automation id is required")
		return
	}
	var body struct {
		Identifier string `json:"identifier"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	identifier := strings.TrimSpace(body.Identifier)
	if identifier == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "identifier is required")
		return
	}
	if err := s.client.TestAutomation(r.Context(), automationID, identifier); err != nil {
		writeClientError(w, "test_automation_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeAutomationValidationError(w http.ResponseWriter, err error) {
	msg := err.Error()
	// Each branch maps a server-side validation error to its form field name
	// on the dashboard, so the typed client (SettingsError.field) can pin the
	// inline error to the correct input rather than render it as a toast (T-34).
	// Identifier-regex and input-context-regex share a code but split fields.
	switch {
	case strings.Contains(msg, "duplicate automation id"):
		writeErrorWithField(w, http.StatusBadRequest, "duplicate_automation_id", msg, "id")
	case strings.Contains(msg, "invalid cron"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_cron", msg, "cron")
	case strings.Contains(msg, "invalid timezone"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_timezone", msg, "timezone")
	case strings.Contains(msg, "invalid identifier_regex"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_regex", msg, "identifierRegex")
	case strings.Contains(msg, "invalid input_context_regex"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_regex", msg, "inputContextRegex")
	case strings.Contains(msg, "unsupported trigger type"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_trigger_type", msg, "triggerType")
	case strings.Contains(msg, "filter.match_mode"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_match_mode", msg, "matchMode")
	case strings.Contains(msg, "filter.limit"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_limit", msg, "limit")
	case strings.Contains(msg, "policy.auto_switch") || strings.Contains(msg, "policy.auto_resume"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_policy", msg, "autoResume")
	case strings.Contains(msg, "switch_to_profile"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_policy", msg, "switchToProfile")
	case strings.Contains(msg, "policy.switch_to_backend"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_policy", msg, "switchToBackend")
	case strings.Contains(msg, "policy.cooldown_minutes"):
		writeErrorWithField(w, http.StatusBadRequest, "invalid_policy", msg, "cooldownMinutes")
	default:
		writeError(w, http.StatusBadRequest, "bad_request", msg)
	}
}

// handleClearAllWorkspaces removes all workspace directories under workspace.root.
// Responds 202 immediately and performs deletion in a background goroutine so
// the UI does not hang on large workspace trees.
// DELETE /api/v1/workspaces
//
// Only one clear runs at a time (CORE-114): a request while one is in flight
// answers 409 clear_in_progress.
func (s *Server) handleClearAllWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !s.clearAllInFlight.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "clear_in_progress", "a workspace clear is already running; retry when it finishes")
		return
	}
	go func() {
		defer s.clearAllInFlight.Store(false)
		// CORE-008: a panic in the client call must not crash the daemon,
		// and must still publish this task's failure outcome — the same log
		// line the error path emits (the 202 has already been sent, so the
		// log is the only outcome channel).
		defer recoverServerGoroutine("clear-all-workspaces", func(r any) {
			slog.Error("clear all workspaces failed", "error", fmt.Sprintf("panic: %v", r))
		})
		if err := s.client.ClearAllWorkspaces(); err != nil {
			slog.Error("clear all workspaces failed", "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// handleSetAutoClearWorkspace toggles automatic workspace cleanup after task success.
// POST /api/v1/settings/workspace/auto-clear
// Body: {"enabled": true|false}
func (s *Server) handleSetAutoClearWorkspace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "enabled field is required")
		return
	}
	if err := s.client.SetAutoClearWorkspace(*body.Enabled); err != nil {
		if errors.Is(err, config.ErrAutoClearAutoReviewConflict) {
			writeError(w, http.StatusBadRequest, "invalid_combination", err.Error())
			return
		}
		writeClientError(w, "set_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "autoClearWorkspace": *body.Enabled})
}

// handleSetDepsAnalysisMode updates dependencies.analysis_mode at runtime.
// POST /api/v1/settings/deps-analysis-mode  {"mode":"auto"|"manual"}
func (s *Server) handleSetDepsAnalysisMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	mode := strings.TrimSpace(body.Mode)
	if err := config.ValidateDepsAnalysisMode(mode); err != nil {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", err.Error(), "mode")
		return
	}
	if err := s.client.SetDepsAnalysisMode(mode); err != nil {
		writeClientError(w, "server_error", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": mode})
}

// handleUpdateTrackerStates updates active/terminal/completion states in-memory and in WORKFLOW.md.
// PUT /api/v1/settings/tracker/states
// Body: {"activeStates": [...], "terminalStates": [...], "completionState": "..."}
func (s *Server) handleUpdateTrackerStates(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActiveStates    []string `json:"activeStates"`
		TerminalStates  []string `json:"terminalStates"`
		CompletionState string   `json:"completionState"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if len(body.ActiveStates) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "activeStates must not be empty")
		return
	}
	if err := s.client.UpdateTrackerStates(body.ActiveStates, body.TerminalStates, body.CompletionState); err != nil {
		writeClientError(w, "update_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAddSSHHost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host        string `json:"host"`
		Description string `json:"description"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if strings.TrimSpace(body.Host) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "host is required")
		return
	}
	if err := s.client.AddSSHHost(strings.TrimSpace(body.Host), body.Description); err != nil {
		writeClientError(w, "add_ssh_host_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRemoveSSHHost(w http.ResponseWriter, r *http.Request) {
	// chi v5 extracts params from RawPath when set, so the value may still be
	// percent-encoded (e.g. "user%40host" instead of "user@host"). Decode before
	// comparing against the stored host string.
	host, err := url.PathUnescape(chi.URLParam(r, "host"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_host", "malformed host encoding in URL")
		return
	}
	if err := s.client.RemoveSSHHost(host); err != nil {
		writeClientError(w, "remove_ssh_host_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetDispatchStrategy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Strategy string `json:"strategy"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	switch body.Strategy {
	case "round-robin", "least-loaded":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "strategy must be round-robin or least-loaded")
		return
	}
	if err := s.client.SetDispatchStrategy(body.Strategy); err != nil {
		writeClientError(w, "set_strategy_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("writeJSON: marshal failed", "type", fmt.Sprintf("%T", v), "error", err)
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// writeJSON sets Content-Length BEFORE WriteHeader, which is incompatible
// with mid-stream use (SSE handlers that have already started writing
// `text/event-stream` framing must NOT call writeJSON afterward — Go would
// emit a `superfluous WriteHeader` warning and the response framing would
// be corrupt). For mid-SSE error reporting use a typed `event: error`
// frame (see writeSubLogErrorEvent for the pattern). G-05 (gaps_280426_2).
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeErrorWithField(w, status, code, message, "")
}

// maxRequestBody is the per-handler upper bound on JSON request body size.
// 1 MiB is generous for every existing settings/automation payload (the
// largest in practice is a fully-populated `automations:` block, well under
// 100 KiB) and bounds memory pressure from pathological / hostile clients
// streaming arbitrary bytes into json.Decode. G-02 (gaps_280426_2).
const maxRequestBody = 1 << 20

// decodeJSONBody wraps r.Body in http.MaxBytesReader before decoding so a
// runaway client cannot OOM the daemon. Callers stay structurally identical
// to the prior `json.NewDecoder(r.Body).Decode(&body)` form. G-02.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	return json.NewDecoder(r.Body).Decode(dst)
}

// writeErrorWithField writes the standard {error:{code,message}} body and
// optionally attaches a `field` discriminator so a typed client (e.g. the
// SettingsError on the dashboard) can pin the error to a specific form
// input rather than rendering it as a generic toast (T-34).
func writeErrorWithField(w http.ResponseWriter, status int, code, message, field string) {
	body := map[string]string{
		"code":    code,
		"message": message,
	}
	if field != "" {
		body["field"] = field
	}
	writeJSON(w, status, map[string]any{"error": body})
}
