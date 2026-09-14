package domain

import "time"

// BufLogEntry is the JSON-encoded log buffer entry shared between the orchestrator
// (written by formatBufLine) and the server (read by parseLogLine).
// The JSON schema is stable; changing field names or omitempty tags requires
// updating both write and read paths simultaneously.
type BufLogEntry struct {
	Level       string `json:"level"`
	Msg         string `json:"msg"`
	Time        string `json:"time"`
	Text        string `json:"text,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	ExitCode    string `json:"exit_code,omitempty"`
	OutputSize  string `json:"output_size,omitempty"`
	Task        string `json:"task,omitempty"`
	URL         string `json:"url,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Detail      string `json:"detail,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

// IssueLogEntry is the normalized log entry returned by the sublogs API.
// It matches the JSON schema expected by the frontend (same as the existing
// /api/v1/issues/{id}/logs response).
type IssueLogEntry struct {
	Level     string `json:"level"`
	Event     string `json:"event"`
	Message   string `json:"message"`
	Tool      string `json:"tool,omitempty"`
	Time      string `json:"time,omitempty"`
	Detail    string `json:"detail,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

// Comment — a comment on a tracker issue.
type Comment struct {
	ID         string
	Body       string
	CreatedAt  *time.Time
	AuthorID   string
	AuthorName string
}

// Issue — normalized tracker record.
type Issue struct {
	ID          string
	Identifier  string
	Title       string
	Description *string
	Priority    *int
	State       string
	BranchName  *string
	URL         *string
	Labels      []string
	BlockedBy   []BlockerRef
	// Comments MUST be returned by tracker adapters in ascending CreatedAt order
	// (oldest first). Downstream consumers — notably latestAutomationComment in
	// cmd/itervox — take the last element as the newest. Linear enforces this via
	// its GraphQL `orderBy: createdAt` parameter; GitHub's comment pagination
	// defaults to ascending order. If a new tracker adapter cannot guarantee this,
	// it must sort before returning.
	Comments  []Comment
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

// BlockerRef — a lightweight reference to a blocking issue.
//
// Origin is an optional provenance marker set by the tracker adapter at
// normalize time. "" (the zero value) means "no adapter-supplied origin" —
// internal/orchestrator/dependency_audit.go::dependencySourceForBlocker
// falls back to its existing text-vs-relation heuristic in that case. Linear
// sets Origin to "sub_issue" for BlockerRefs derived from a parent's
// children (see internal/tracker/linear/normalize.go), which maps to the
// dedicated DependencySourceSubIssue audit label so operators can tell
// sub-issue gating apart from an explicit "blocks" relation.
type BlockerRef struct {
	ID         *string
	Identifier *string
	State      *string
	URL        *string
	Origin     string
}

// BlockerOriginSubIssue is the BlockerRef.Origin value for refs derived from
// a parent issue's sub-issues. The single shared constant exists because
// three packages (tracker/linear sets it, orchestrator maps it, and the
// audit-source label mirrors it) must agree on the string — a bare literal
// in any of them could drift with no compiler signal.
const BlockerOriginSubIssue = "sub_issue"

// Project is a generic project/team/repository grouping returned by trackers
// that support project-level filtering (e.g. Linear workspaces).
type Project struct {
	ID   string
	Name string
	Slug string
}
