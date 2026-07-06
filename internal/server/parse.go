package server

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
)

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
		// `AUTOMATION FIRED · <id>` blocks must surface
		// as a dedicated event so the frontend's automation chip catches them.
		if strings.HasPrefix(e.Msg, "AUTOMATION FIRED · ") {
			entry.Event = "automation"
			entry.Message = e.Msg
			break
		}
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
