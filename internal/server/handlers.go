package server

import (
	"bytes"
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
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

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
// A keep-alive comment (": ping\n\n") is sent every 25 s to prevent proxy timeouts.
// GET /api/v1/events
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
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
		case <-sub:
			if err := s.writeSSEEvent(w, flusher); err != nil {
				return
			}
			ticker.Reset(keepaliveInterval) // §7.2 — defer keepalive after activity
		case <-ticker.C:
			// G-04 (gaps_280426_2): set a per-tick write deadline so a
			// half-closed TCP connection (proxy gone, client crashed) is
			// detected within the deadline rather than waiting for OS-level
			// keepalive. ResponseController returns ErrNotSupported on net
			// implementations that don't support deadlines (rare in practice);
			// we ignore the error and fall through to the legacy behavior.
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
			// Send SSE keepalive as a NAMED event (not a comment) so the
			// client's @microsoft/fetch-event-source delivers it to onMessage.
			// Comments (`: ping`) are stripped by the SSE parser per spec —
			// using them meant the dashboard's silence watchdog could not
			// distinguish "no real updates" from "connection dead", so the
			// "Reconnecting…" banner kept appearing on quiet systems. The
			// payload is intentionally tiny; the client checks event type
			// and short-circuits without re-parsing the snapshot.
			if _, err := fmt.Fprintf(w, "event: keepalive\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) writeSSEEvent(w http.ResponseWriter, flusher http.Flusher) error {
	snap := s.snapshot()
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// handleReanalyzeIssue moves a paused issue to the forced re-analysis queue,
// bypassing the open-PR guard on next dispatch.
// POST /api/v1/issues/{identifier}/reanalyze
func (s *Server) handleReanalyzeIssue(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if !s.client.ReanalyzeIssue(identifier) {
		writeError(w, http.StatusNotFound, "not_paused", "issue "+identifier+" is not paused")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": true, "identifier": identifier})
}

// handleResumeIssue removes a paused issue from the pause set so it can be dispatched again.
func (s *Server) handleResumeIssue(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if s.client.ResumeIssue(identifier) {
		writeJSON(w, http.StatusOK, map[string]any{"resumed": true, "identifier": identifier})
	} else {
		writeError(w, http.StatusNotFound, "not_paused", "issue "+identifier+" is not paused")
	}
}

// handleCancelIssue cancels the running worker for the given issue identifier.
func (s *Server) handleCancelIssue(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if s.client.CancelIssue(identifier) {
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true, "identifier": identifier})
	} else {
		writeError(w, http.StatusNotFound, "not_running", "issue "+identifier+" is not running")
	}
}

// handleTerminateIssue hard-stops a running or paused issue without adding it to PausedIdentifiers.
func (s *Server) handleTerminateIssue(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if s.client.TerminateIssue(identifier) {
		writeJSON(w, http.StatusOK, map[string]any{"terminated": true, "identifier": identifier})
	} else {
		writeError(w, http.StatusNotFound, "not_found", "issue "+identifier+" is not running or paused")
	}
}

// handleIssueDetail returns a single issue by identifier, enriched with orchestrator state.
func (s *Server) handleIssueDetail(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")

	// Fast path: use the single-item callback when available.
	if s.fetchIssue != nil {
		issue, err := s.fetchIssue(r.Context(), identifier)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "fetch_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "fetch_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "fetch_failed", err.Error())
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	f, err := os.Open(s.logFile)
	if err != nil {
		_, _ = fmt.Fprintf(w, "event: log\ndata: [log file not yet available: %s]\n\n", err)
		flusher.Flush()
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

	flushPending := func() {
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
			_, _ = fmt.Fprintf(w, "event: log\ndata: %s\n\n", line)
		}
	}

	flush := func() {
		for {
			n, err := f.Read(readBuf)
			if n > 0 {
				if pending.Len()+n > maxPending {
					// Flush what we have before accepting more so data is not dropped.
					flushPending()
				}
				pending.Write(readBuf[:n])
			}
			// Send complete lines.
			flushPending()
			if err != nil || n == 0 {
				// n == 0 with err == nil means no new data (EOF on regular file);
				// break to avoid a busy-spin until the next ticker tick (GO-R10-5).
				break
			}
		}
		flusher.Flush()
	}

	flush() // send initial tail immediately
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			flush()
		}
	}
}

// handleIssueLogs returns parsed log entries for a specific issue identifier
// from the in-memory log buffer (only available for currently-running sessions).
func (s *Server) handleIssueLogs(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	lines := s.client.FetchLogs(identifier)
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

// handleIssueLogStream streams parsed log entries for one issue as SSE.
// It tracks a cursor into the in-memory buffer and emits only new entries on
// each tick, so clients receive push notifications instead of polling.
// If the buffer is reset (cleared/issue removed) the cursor resets and all
// current entries are re-sent.
// GET /api/v1/issues/{identifier}/log-stream
func (s *Server) handleIssueLogStream(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// cursor tracks how many lines from the buffer have already been sent.
	// On reconnect we honor the Last-Event-ID header (T-18) so the client
	// resumes after the last event it acknowledged. Browsers (and the
	// @microsoft/fetch-event-source library used by web/) automatically
	// echo this header on reconnect when the server emits "id:" lines.
	sent := parseLastEventID(r.Header.Get("Last-Event-ID"))

	sendNew := func() bool {
		lines := s.client.FetchLogs(identifier)
		// Guard against buffer reset (cleared while streaming) or a stale
		// Last-Event-ID pointing past the current buffer length.
		if sent > len(lines) {
			sent = 0
		}
		for _, line := range lines[sent:] {
			sent++
			entry, skip := parseLogLine(line)
			if skip {
				continue
			}
			b, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			// id: <cursor> lets the client resume from this point on
			// reconnect via the Last-Event-ID header.
			if _, err := fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", sent, b); err != nil {
				return false
			}
		}
		flusher.Flush()
		return true
	}

	if !sendNew() {
		return
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !sendNew() {
				return
			}
		}
	}
}

// parseLastEventID reads a numeric Last-Event-ID header value. Empty,
// negative, or non-numeric values resolve to 0 (replay from beginning) —
// the conservative default that callers fall through to.
func parseLastEventID(h string) int {
	if h == "" {
		return 0
	}
	n, err := strconv.Atoi(h)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// handleSubLogStream streams parsed session/subagent log entries for one issue
// as SSE. The source data still comes from per-issue JSONL files, but the
// browser receives push updates instead of polling every few seconds.
// GET /api/v1/issues/{identifier}/sublog-stream
func (s *Server) handleSubLogStream(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	initialEntries, err := s.client.FetchSubLogs(identifier)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "fetch_failed", err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Honor Last-Event-ID for resume-after-reconnect (T-18).
	sent := parseLastEventID(r.Header.Get("Last-Event-ID"))
	sendNew := func(entries []domain.IssueLogEntry) bool {
		if sent > len(entries) {
			sent = 0
		}
		for _, entry := range entries[sent:] {
			sent++
			b, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: sublog\ndata: %s\n\n", sent, b); err != nil {
				return false
			}
		}
		flusher.Flush()
		return true
	}

	if !sendNew(initialEntries) {
		return
	}

	// G-01 (gaps_280426_2): 5-second cadence (was 1 second). FetchSubLogs
	// re-reads + re-parses every `.jsonl` line in the per-issue session
	// directory on every tick, scaling with `(open viewers × session size)`.
	// 5s is a stop-gap that 5x-reduces the disk/CPU cost while keeping
	// dashboard latency tolerable (most sublog activity is multi-second
	// agent reasoning, not sub-second). A proper fix tracks per-stream file
	// offsets and only reads appended bytes; deferred to a future T-NN.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			entries, err := s.client.FetchSubLogs(identifier)
			if err != nil {
				// T-45 (03.G-06): emit a structured SSE `error` frame before
				// returning so the dashboard can distinguish a tracker fetch
				// failure from a clean disconnect (user closed tab). Without
				// this, the per-issue sublog modal silently disappears with
				// no signal of what went wrong.
				writeSubLogErrorEvent(w, err)
				return
			}
			if !sendNew(entries) {
				return
			}
		}
	}
}

// writeSubLogErrorEvent emits an SSE `event: error` frame carrying a JSON
// payload `{code, message}` so the dashboard can render a toast or banner
// instead of treating the disconnect as benign. T-45 (03.G-06).
func writeSubLogErrorEvent(w http.ResponseWriter, err error) {
	const code = "fetch_failed"
	// JSON-encode inline to keep this self-contained — the payload is small
	// enough that pulling in encoding/json's error path is overkill.
	msg := err.Error()
	// Replace characters that would break the SSE single-line data: framing.
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	// Produce a compact JSON envelope; quoting via fmt.Sprintf is safe here
	// because both code and the cleaned message are plain ASCII strings —
	// %q escapes embedded quotes for us.
	payload := fmt.Sprintf(`{"code":%q,"message":%q}`, code, msg)
	if _, werr := fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload); werr == nil {
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// handleClearIssueLogs deletes the in-memory and on-disk log buffer for an issue.
// DELETE /api/v1/issues/{identifier}/logs
func (s *Server) handleClearIssueLogs(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if err := s.client.ClearLogs(identifier); err != nil {
		writeError(w, http.StatusInternalServerError, "clear_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleClearIssueSubLogs deletes all JSONL session files for one issue.
// DELETE /api/v1/issues/{identifier}/sublogs
func (s *Server) handleClearIssueSubLogs(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if err := s.client.ClearIssueSubLogs(identifier); err != nil {
		writeError(w, http.StatusInternalServerError, "clear_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "clear_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "clear_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// skipEntry returns true for internal lifecycle events that are noise in the timeline.
// Operates on already-parsed BufLogEntry fields rather than string-prefix matching.
func skipEntry(e bufLogEntry) bool {
	if e.Level == "DEBUG" {
		return true
	}
	switch e.Msg {
	case "claude: session started", "claude: turn done",
		"codex: session started", "codex: turn done":
		return true
	}
	return false
}

// buildDetailJSON builds a compact JSON detail string for shell completions.
// Fields are omitted when empty so the Detail field stays minimal.
// Uses a struct for deterministic key ordering in the JSON output.
func buildDetailJSON(status, exitCode, outputSize string) string {
	type detail struct {
		Status     string `json:"status,omitempty"`
		ExitCode   *int   `json:"exit_code,omitempty"`
		OutputSize *int   `json:"output_size,omitempty"`
	}
	d := detail{Status: status}
	if exitCode != "" {
		if n, err := strconv.Atoi(exitCode); err == nil {
			d.ExitCode = &n
		}
	}
	if outputSize != "" {
		if n, err := strconv.Atoi(outputSize); err == nil {
			d.OutputSize = &n
		}
	}
	if d.Status == "" && d.ExitCode == nil && d.OutputSize == nil {
		return ""
	}
	b, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	return string(b)
}

// bufLogEntry is a package-local alias for domain.BufLogEntry.
// The canonical definition lives in internal/domain, shared with the orchestrator.
type bufLogEntry = domain.BufLogEntry

// parseLogLine converts a JSON log buffer line into a structured IssueLogEntry.
// Returns (entry, false) for valid entries, (zero, true) to signal skip.
func parseLogLine(line string) (IssueLogEntry, bool) {
	var e bufLogEntry
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		// Non-JSON line (e.g. legacy entry) — skip rather than panic.
		return IssueLogEntry{}, true
	}

	if skipEntry(e) {
		return IssueLogEntry{}, true
	}

	entry := IssueLogEntry{Level: e.Level, Time: e.Time, SessionID: e.SessionID}

	switch e.Msg {
	case "claude: text", "codex: text":
		entry.Event = "text"
		entry.Message = e.Text
	case "claude: subagent", "codex: subagent":
		entry.Event = "subagent"
		entry.Tool = e.Tool
		entry.Message = e.Description
		if entry.Message == "" {
			entry.Message = e.Tool + " (subagent)"
		}
	case "claude: action_started", "codex: action_started":
		entry.Event = "action"
		entry.Tool = e.Tool
		entry.Message = e.Tool + "…"
		if e.Description != "" {
			entry.Message = e.Tool + " — " + e.Description + "…"
		}
	case "claude: action_detail", "codex: action_detail":
		entry.Event = "action"
		entry.Tool = e.Tool
		entry.Detail = buildDetailJSON(e.Status, e.ExitCode, e.OutputSize)
		entry.Message = e.Tool + " completed"
		if e.ExitCode != "" && e.ExitCode != "0" {
			entry.Message = e.Tool + " failed (exit:" + e.ExitCode + ")"
		}
	case "claude: action", "codex: action":
		entry.Event = "action"
		entry.Tool = e.Tool
		entry.Message = e.Tool
		if e.Description != "" {
			entry.Message = e.Tool + " — " + e.Description
		}
	case "claude: todo", "codex: todo":
		entry.Event = "action"
		entry.Tool = "TodoWrite"
		task := e.Task
		if task == "" {
			task = e.Msg
		}
		entry.Message = "☐ " + task
	case "worker: pr_opened":
		entry.Event = "pr"
		entry.Message = "✓ PR opened: " + e.URL
	case "worker: turn_summary":
		entry.Event = "turn"
		entry.Message = e.Summary
	case "worker: turn failed":
		entry.Event = "error"
		if e.Detail != "" {
			entry.Message = e.Detail
		} else {
			entry.Message = "turn failed"
		}
	default:
		switch e.Level {
		case "ERROR":
			entry.Event = "error"
			entry.Message = e.Msg
		case "WARN":
			entry.Event = "warn"
			entry.Message = e.Msg
		default:
			entry.Event = "info"
			entry.Message = e.Msg
		}
	}

	return entry, false
}

// handleSubLogs returns parsed session log entries from CLAUDE_CODE_LOG_DIR files.
// This endpoint reads .jsonl stream-json files written by Claude Code when
// CLAUDE_CODE_LOG_DIR is set, covering all subagents spawned during the session.
// Returns an empty array when no logs exist (not an error).
// GET /api/v1/issues/{identifier}/sublogs
func (s *Server) handleSubLogs(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	entries, err := s.client.FetchSubLogs(identifier)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "fetch_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "dispatch_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "fetch_failed", err.Error())
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
	if body.Slugs == nil {
		s.projectManager.SetProjectFilter(nil) // reset to WORKFLOW.md default
	} else {
		s.projectManager.SetProjectFilter(*body.Slugs)
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
	if err := s.client.UpdateIssueState(r.Context(), identifier, body.State); err != nil {
		writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
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
	s.client.SetIssueBackend(identifier, body.Backend)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "identifier": identifier, "backend": body.Backend})
}

func (s *Server) handleProvideInput(w http.ResponseWriter, r *http.Request) {
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
	if ok := s.client.ProvideInput(identifier, body.Message); !ok {
		writeError(w, http.StatusNotFound, "not_found", "issue not in input-required state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDismissInput(w http.ResponseWriter, r *http.Request) {
	identifier := chi.URLParam(r, "identifier")
	if ok := s.client.DismissInput(identifier); !ok {
		writeError(w, http.StatusNotFound, "not_found", "issue not in input-required state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
		writeError(w, http.StatusInternalServerError, "comment_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "create_issue_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "issue": issue})
}

func (s *Server) handleAgentMoveState(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.validateAgentActionRequest(w, r, config.AgentActionMoveState); !ok {
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
	if err := s.client.UpdateIssueState(r.Context(), identifier, body.State); err != nil {
		writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
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
	if ok := s.client.ProvideInput(identifier, body.Message); !ok {
		writeError(w, http.StatusNotFound, "not_found", "issue not in input-required state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
		writeError(w, http.StatusInternalServerError, "server_error", err.Error())
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
			writeError(w, http.StatusInternalServerError, "persist_failed", err.Error())
			return
		}
	} else {
		// Relative delta: use BumpMaxWorkers for an atomic read-modify-write.
		var err error
		target, err = s.client.BumpWorkers(body.Delta)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "persist_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "persist_failed", err.Error())
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

// handleUpsertProfile creates or updates a named agent profile.
// PUT /api/v1/settings/profiles/{name}
// Body: {"command": "claude --model ...", "prompt": "...", "backend": "codex"}
func (s *Server) handleUpsertProfile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Command          string   `json:"command"`
		Prompt           string   `json:"prompt"`
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
		Backend:          body.Backend,
		Enabled:          body.Enabled == nil || *body.Enabled,
		AllowedActions:   config.NormalizeAllowedActions(body.AllowedActions),
		CreateIssueState: strings.TrimSpace(body.CreateIssueState),
	}
	if err := s.client.UpsertProfile(name, def, strings.TrimSpace(body.OriginalName)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			writeError(w, http.StatusConflict, "profile_exists", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "upsert_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteProfile removes a named agent profile.
// DELETE /api/v1/settings/profiles/{name}
func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := s.client.DeleteProfile(name); err != nil {
		writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
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
	if err := config.ValidateAutomations(automationConfigsFromDefs(body.Automations), profiles); err != nil {
		writeAutomationValidationError(w, err)
		return
	}
	if err := s.client.SetAutomations(body.Automations); err != nil {
		writeError(w, http.StatusInternalServerError, "set_automations_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "test_automation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func automationConfigsFromDefs(defs []AutomationDef) []config.AutomationConfig {
	if len(defs) == 0 {
		return nil
	}
	automations := make([]config.AutomationConfig, 0, len(defs))
	for _, def := range defs {
		automations = append(automations, config.AutomationConfig{
			ID:           def.ID,
			Enabled:      def.Enabled,
			Profile:      def.Profile,
			Instructions: def.Instructions,
			Trigger: config.AutomationTriggerConfig{
				Type:     def.Trigger.Type,
				Cron:     def.Trigger.Cron,
				Timezone: def.Trigger.Timezone,
				State:    def.Trigger.State,
			},
			Filter: config.AutomationFilterConfig{
				MatchMode:         def.Filter.MatchMode,
				States:            def.Filter.States,
				LabelsAny:         def.Filter.LabelsAny,
				IdentifierRegex:   def.Filter.IdentifierRegex,
				Limit:             def.Filter.Limit,
				InputContextRegex: def.Filter.InputContextRegex,
				MaxAgeMinutes:     def.Filter.MaxAgeMinutes,
			},
			Policy: config.AutomationPolicyConfig{
				AutoResume:      def.Policy.AutoResume,
				SwitchToProfile: def.Policy.SwitchToProfile,
				SwitchToBackend: def.Policy.SwitchToBackend,
				CooldownMinutes: def.Policy.CooldownMinutes,
			},
		})
	}
	return automations
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
func (s *Server) handleClearAllWorkspaces(w http.ResponseWriter, r *http.Request) {
	go func() {
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
		writeError(w, http.StatusInternalServerError, "set_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "autoClearWorkspace": *body.Enabled})
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
		writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "add_ssh_host_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "remove_ssh_host_failed", err.Error())
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
		writeError(w, http.StatusInternalServerError, "set_strategy_failed", err.Error())
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
