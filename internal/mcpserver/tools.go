package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// tool is one MCP tool: its listing and its handler.
type tool struct {
	name        string
	description string
	// scope is the token scope the tool needs: "viewer" or "operator".
	scope  string
	schema map[string]any
	run    func(ctx context.Context, c *Client, args json.RawMessage) (any, error)
}

// Tool scopes.
const (
	scopeViewer   = "viewer"
	scopeOperator = "operator"
)

// Output bounds: results are compact, never a transcript.
const (
	maxTextBytes        = 600  // a title, question, reason or failure message
	maxDescriptionBytes = 4000 // an issue description
	defaultListLimit    = 50
	maxListLimit        = 200
	maxIdentifierBytes  = 128
	maxMessageBytesTool = 10 * 1024 // the daemon's own cap for replies and steering
)

// refusal is a tool error the server chose (not a failure): the tool's
// rules say no.
type refusal struct{ msg string }

func (r *refusal) Error() string { return r.msg }

func refuse(format string, a ...any) error { return &refusal{msg: fmt.Sprintf(format, a...)} }

// tools is every tool, read tools first. tools/list shows those the
// caller's scope allows.
var tools []tool

func init() {
	tools = append(readTools(), writeTools()...)
}

func toolByName(name string) (tool, bool) {
	i := slices.IndexFunc(tools, func(t tool) bool { return t.name == name })
	if i < 0 {
		return tool{}, false
	}
	return tools[i], true
}

// ToolNames lists the tools a caller of scope may use, in listing order.
func ToolNames(scope string) []string {
	var out []string
	for _, t := range tools {
		if allowed(scope, t.scope) {
			out = append(out, t.name)
		}
	}
	return out
}

// object builds an input JSON Schema.
func object(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func integer(desc string, minimum, maximum int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "minimum": minimum, "maximum": maximum}
}

var identifierProp = str("Issue identifier, e.g. ENG-12 or #42")

// decode reads a tool's arguments strictly.
func decode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return refuse("invalid arguments: %v", err)
	}
	return nil
}

func checkIdentifier(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxIdentifierBytes || strings.ContainsAny(id, "/\\\x00\n\r") {
		return "", refuse("identifier is required (at most %d characters)", maxIdentifierBytes)
	}
	return id, nil
}

func issuePath(id string) string { return "/issues/" + url.PathEscape(id) }

func limitOr(n, def int) int {
	if n <= 0 {
		return def
	}
	return min(n, maxListLimit)
}

// stateView is the part of GET /api/v1/state the tools read.
type stateView struct {
	GeneratedAt time.Time `json:"generatedAt"`
	ProjectName string    `json:"projectName"`
	Counts      struct {
		Running  int `json:"running"`
		Retrying int `json:"retrying"`
		Paused   int `json:"paused"`
	} `json:"counts"`
	Running []struct {
		Identifier string    `json:"identifier"`
		Kind       string    `json:"kind"`
		Backend    string    `json:"backend"`
		WorkerHost string    `json:"workerHost"`
		TurnCount  int       `json:"turnCount"`
		Tokens     int       `json:"tokens"`
		StartedAt  time.Time `json:"startedAt"`
	} `json:"running"`
	Retrying []struct {
		Identifier string    `json:"identifier"`
		Attempt    int       `json:"attempt"`
		DueAt      time.Time `json:"dueAt"`
		Error      string    `json:"error"`
	} `json:"retrying"`
	Paused              []string `json:"paused"`
	MaxConcurrentAgents int      `json:"maxConcurrentAgents"`
	ActiveStates        []string `json:"activeStates"`
	InputRequired       []struct {
		Identifier string `json:"identifier"`
		State      string `json:"state"`
		Context    string `json:"context"`
		Profile    string `json:"profile"`
		AgeMinutes int    `json:"ageMinutes"`
	} `json:"inputRequired"`
	DispatchPaused *pauseView `json:"dispatchPaused"`
	RecentFailures []struct {
		Kind       string    `json:"kind"`
		Identifier string    `json:"identifier"`
		Source     string    `json:"source"`
		Message    string    `json:"message"`
		OccurredAt time.Time `json:"occurredAt"`
		Count      int       `json:"count"`
	} `json:"recentFailures"`
	DependencyAudit []struct {
		Identifier         string     `json:"identifier"`
		IssueState         string     `json:"issueState"`
		Status             string     `json:"status"`
		UnresolvedBlockers []blockRef `json:"unresolvedBlockers"`
		FirstBlockedAt     *time.Time `json:"firstBlockedAt"`
		Degraded           bool       `json:"degraded"`
	} `json:"dependencyAudit"`
	DependencyGraphEdges []struct {
		SourceIdentifier string  `json:"sourceIdentifier"`
		TargetIdentifier string  `json:"targetIdentifier"`
		SourceState      string  `json:"sourceState"`
		Resolved         bool    `json:"resolved"`
		Origin           string  `json:"origin"`
		Evidence         string  `json:"evidence"`
		Confidence       float64 `json:"confidence"`
		Stale            bool    `json:"stale"`
		Overridden       bool    `json:"overridden"`
		Gating           bool    `json:"gating"`
	} `json:"dependencyGraphEdges"`
	DependencyCycles []struct {
		Members []string `json:"members"`
		Kind    string   `json:"kind"`
	} `json:"dependencyCycles"`
	DependencyAttention []struct {
		Identifier   string    `json:"identifier"`
		Blockers     []string  `json:"blockers"`
		BlockedSince time.Time `json:"blockedSince"`
		Kind         string    `json:"kind"`
	} `json:"dependencyAttention"`
	FeedSeq int64 `json:"feedSeq"`
}

type blockRef struct {
	Identifier string `json:"identifier"`
	State      string `json:"state"`
}

// pauseView is the daemon-wide dispatch pause (#213).
type pauseView struct {
	Reason   string     `json:"reason,omitempty"`
	Until    *time.Time `json:"until,omitempty"`
	PausedAt time.Time  `json:"pausedAt"`
}

func (p *pauseView) redacted() *pauseView {
	if p == nil {
		return nil
	}
	out := *p
	out.Reason = clip(p.Reason, maxTextBytes)
	return &out
}

func getState(ctx context.Context, c *Client) (stateView, error) {
	var st stateView
	err := c.Do(ctx, http.MethodGet, "/state", nil, nil, &st)
	return st, err
}

// issueView is the part of a TrackerIssue (GET /api/v1/issues) the tools
// return. lastMessage (agent output) is never read.
type issueView struct {
	Identifier        string   `json:"identifier"`
	Title             string   `json:"title"`
	State             string   `json:"state"`
	Description       string   `json:"description,omitempty"`
	URL               string   `json:"url,omitempty"`
	OrchestratorState string   `json:"orchestratorState"`
	Labels            []string `json:"labels,omitempty"`
	Priority          *int     `json:"priority,omitempty"`
	BlockedBy         []string `json:"blockedBy,omitempty"`
	IneligibleReason  string   `json:"ineligibleReason,omitempty"`
	AgentProfile      string   `json:"agentProfile,omitempty"`
	Error             string   `json:"error,omitempty"`
	TurnCount         int      `json:"turnCount,omitempty"`
	Tokens            int      `json:"tokens,omitempty"`
}

func (v issueView) compact(withDescription bool) map[string]any {
	out := map[string]any{
		"identifier": v.Identifier, "title": clip(v.Title, maxTextBytes), "state": v.State,
		"orchestratorState": v.OrchestratorState,
	}
	setIf(out, "url", v.URL)
	setIf(out, "labels", v.Labels)
	if v.Priority != nil {
		out["priority"] = *v.Priority
	}
	setIf(out, "blockedBy", v.BlockedBy)
	setIf(out, "ineligibleReason", v.IneligibleReason)
	setIf(out, "profile", v.AgentProfile)
	setIf(out, "error", clip(v.Error, maxTextBytes))
	if withDescription {
		setIf(out, "description", clip(v.Description, maxDescriptionBytes))
		if v.TurnCount > 0 {
			out["turnCount"] = v.TurnCount
		}
		if v.Tokens > 0 {
			out["tokens"] = v.Tokens
		}
	}
	return out
}

// setIf sets out[k] = v unless v is empty.
func setIf[T any](out map[string]any, k string, v T) {
	switch x := any(v).(type) {
	case string:
		if x == "" {
			return
		}
	case []string:
		if len(x) == 0 {
			return
		}
	}
	out[k] = v
}
