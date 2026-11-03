package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// contextView is GET /api/v1/issues/{id}/context (#215).
type contextView struct {
	Run *struct {
		RunID        string    `json:"runId"`
		Live         bool      `json:"live"`
		Kind         string    `json:"kind"`
		Profile      string    `json:"profile"`
		Backend      string    `json:"backend"`
		StartedAt    time.Time `json:"startedAt"`
		FinishedAt   time.Time `json:"finishedAt"`
		Outcome      string    `json:"outcome"`
		CurrentStep  string    `json:"currentStep"`
		LastToolCall *struct {
			Name   string `json:"name"`
			Target string `json:"target"`
		} `json:"lastToolCall"`
		ToolCalls         int    `json:"toolCalls"`
		TurnCount         int    `json:"turnCount"`
		TotalTokens       int    `json:"totalTokens"`
		PendingInput      string `json:"pendingInput"`
		PendingApproval   bool   `json:"pendingApproval"`
		QueuedSteers      int    `json:"queuedSteers"`
		GuardrailBlocks   int    `json:"guardrailBlocks"`
		LastGuardrailKind string `json:"lastGuardrailKind"`
	} `json:"run"`
	ChangedFiles []struct {
		Path      string `json:"path"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	} `json:"changedFiles"`
	ChangedFilesError string `json:"changedFilesError"`
	Handoffs          []struct {
		Title   string `json:"title"`
		Path    string `json:"path"`
		Partial bool   `json:"partial"`
	} `json:"handoffs"`
}

// maxChangedFiles bounds the changed files get_issue lists.
const maxChangedFiles = 50

func (v *contextView) compact() map[string]any {
	out := map[string]any{}
	if r := v.Run; r != nil {
		run := map[string]any{"live": r.Live, "turns": r.TurnCount, "toolCalls": r.ToolCalls, "tokens": r.TotalTokens}
		setIf(run, "runId", r.RunID)
		setIf(run, "kind", r.Kind)
		setIf(run, "profile", r.Profile)
		setIf(run, "backend", r.Backend)
		setIf(run, "outcome", r.Outcome)
		setIf(run, "currentStep", clip(r.CurrentStep, maxTextBytes))
		if !r.StartedAt.IsZero() {
			run["startedAt"] = r.StartedAt
		}
		if !r.FinishedAt.IsZero() {
			run["finishedAt"] = r.FinishedAt
		}
		if t := r.LastToolCall; t != nil {
			run["lastToolCall"] = strings.TrimSpace(t.Name + " " + clip(t.Target, maxTextBytes))
		}
		setIf(run, "pendingQuestion", clip(r.PendingInput, maxTextBytes))
		if r.PendingApproval {
			run["pendingGuardrailApproval"] = true
		}
		if r.QueuedSteers > 0 {
			run["queuedSteers"] = r.QueuedSteers
		}
		if r.GuardrailBlocks > 0 {
			run["guardrailBlocks"] = r.GuardrailBlocks
			setIf(run, "lastGuardrailKind", r.LastGuardrailKind)
		}
		out["current"] = run
	}
	files := make([]string, 0, min(len(v.ChangedFiles), maxChangedFiles))
	for i, f := range v.ChangedFiles {
		if i == maxChangedFiles {
			break
		}
		files = append(files, f.Status+" "+f.Path+" +"+strconv.Itoa(f.Additions)+"/-"+strconv.Itoa(f.Deletions))
	}
	out["changedFiles"] = files
	if len(v.ChangedFiles) > maxChangedFiles {
		out["changedFilesTotal"] = len(v.ChangedFiles)
	}
	setIf(out, "changedFilesError", v.ChangedFilesError)
	handoffs := make([]string, 0, len(v.Handoffs))
	for _, h := range v.Handoffs {
		name := h.Path
		if h.Partial {
			name += " (partial)"
		}
		handoffs = append(handoffs, name)
	}
	setIf(out, "handoffs", handoffs)
	return out
}

// runContext reads an issue's run context; nil (no error) when the issue
// has no run and no worktree, or the daemon does not serve run context.
func runContext(ctx context.Context, c *Client, id string) (*contextView, error) {
	var v contextView
	err := c.Do(ctx, http.MethodGet, issuePath(id)+"/context", nil, nil, &v)
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusServiceUnavailable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}
