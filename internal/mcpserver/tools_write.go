package mcpserver

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Write tools need an operator token and keep the dashboard's rules:
// dispatch does not override a daemon-wide pause, reply_input never answers
// a guardrail approval (those stay human-only), and remember only proposes
// (the entry waits in the lessons review queue).

// maxPauseMinutes bounds pause_dispatch's minutes (a week).
const maxPauseMinutes = 7 * 24 * 60

// maxMemoryTextBytes mirrors memory.MaxTextBytes.
const maxMemoryTextBytes = 2000

// mcpMemorySource is the provenance kind of entries `remember` proposes.
const mcpMemorySource = "mcp"

func writeTools() []tool {
	idAndMessage := object(map[string]any{
		"identifier": identifierProp,
		"message":    str("The text to send (at most 10 KiB)"),
	}, "identifier", "message")
	return []tool{
		{
			name:        "dispatch",
			description: "Queue an issue for an agent by moving it to the first active state. Refused while all dispatch is paused; never resumes dispatch by itself.",
			scope:       scopeOperator,
			schema:      object(map[string]any{"identifier": identifierProp}, "identifier"),
			run:         dispatch,
		},
		{
			name:        "pause_dispatch",
			description: "Pause ALL dispatch: running agents continue, nothing new starts until resume_dispatch (or after minutes).",
			scope:       scopeOperator,
			schema: object(map[string]any{
				"reason":  str("Why, shown on the dashboard"),
				"minutes": integer("Resume by itself after this many minutes", 1, maxPauseMinutes),
			}),
			run: pauseDispatch,
		},
		{
			name:        "resume_dispatch",
			description: "Resume all dispatch after pause_dispatch or a pause set on the dashboard.",
			scope:       scopeOperator,
			schema:      object(map[string]any{}),
			run:         resumeDispatch,
		},
		{
			name:        "reply_input",
			description: "Answer an agent that is waiting for input. Guardrail approvals are refused: a human answers them on the dashboard.",
			scope:       scopeOperator,
			schema:      idAndMessage,
			run:         replyInput,
		},
		{
			name:        "steer",
			description: "Send a steering message to an issue's running agent (delivered live or with its next turn).",
			scope:       scopeOperator,
			schema:      idAndMessage,
			run:         steer,
		},
		{
			name:        "remember",
			description: "Propose a memory for future runs. It is created as proposed and waits in the lessons review queue until a human approves it.",
			scope:       scopeOperator,
			schema: object(map[string]any{
				"text":  str("What to remember (at most 2000 bytes; secrets are redacted)"),
				"type":  map[string]any{"type": "string", "enum": []string{"fact", "preference", "decision", "lesson"}, "description": "Kind of memory (default fact)"},
				"scope": map[string]any{"type": "string", "enum": []string{"private", "project"}, "description": "private (only you) or project (shared, committed); default: memory.lessons.scope"},
				"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 32, "description": "Tags that help ranking"},
				"issue": str("Issue the memory came from"),
			}, "text"),
			run: remember,
		},
	}
}

type idMessage struct {
	Identifier string `json:"identifier"`
	Message    string `json:"message"`
}

func (a *idMessage) check(raw json.RawMessage) error {
	if err := decode(raw, a); err != nil {
		return err
	}
	id, err := checkIdentifier(a.Identifier)
	if err != nil {
		return err
	}
	a.Identifier = id
	a.Message = strings.TrimSpace(a.Message)
	if a.Message == "" || len(a.Message) > maxMessageBytesTool {
		return refuse("message is required (at most 10 KiB)")
	}
	return nil
}

func dispatch(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a struct {
		Identifier string `json:"identifier"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	id, err := checkIdentifier(a.Identifier)
	if err != nil {
		return nil, err
	}
	st, err := getState(ctx, c)
	if err != nil {
		return nil, err
	}
	if p := st.DispatchPaused.redacted(); p != nil {
		msg := "all dispatch is paused"
		if p.Reason != "" {
			msg += " (" + p.Reason + ")"
		}
		if p.Until != nil {
			msg += " until " + p.Until.UTC().Format(time.RFC3339)
		}
		return nil, refuse("%s: %s was not dispatched. Ask a human whether to resume dispatch first.", msg, id)
	}
	if len(st.ActiveStates) == 0 {
		return nil, refuse("the daemon reports no active states")
	}
	target := st.ActiveStates[0]
	if err := c.Do(ctx, http.MethodPatch, issuePath(id)+"/state", nil, map[string]string{"state": target}, nil); err != nil {
		return nil, err
	}
	out := map[string]any{"identifier": id, "state": target, "queued": true,
		"note": "moved to " + target + "; it starts when a slot is free and its blockers are done"}
	if slices.ContainsFunc(st.Paused, func(p string) bool { return strings.EqualFold(p, id) }) {
		out["note"] = "moved to " + target + ", but the issue itself is paused; a human resumes it on the dashboard"
	}
	return out, nil
}

// dispatchPauseView is the answer of POST /api/v1/dispatch/pause|resume.
type dispatchPauseView struct {
	DispatchPaused *pauseView `json:"dispatchPaused"`
}

func pauseDispatch(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a struct {
		Reason  string `json:"reason"`
		Minutes int    `json:"minutes"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if a.Minutes < 0 || a.Minutes > maxPauseMinutes {
		return nil, refuse("minutes must be between 1 and %d", maxPauseMinutes)
	}
	body := map[string]string{}
	if r := strings.TrimSpace(a.Reason); r != "" {
		body["reason"] = r
	}
	if a.Minutes > 0 {
		body["until"] = time.Now().Add(time.Duration(a.Minutes) * time.Minute).UTC().Format(time.RFC3339)
	}
	var res dispatchPauseView
	if err := c.Do(ctx, http.MethodPost, "/dispatch/pause", nil, body, &res); err != nil {
		return nil, err
	}
	return map[string]any{"dispatchPaused": res.DispatchPaused.redacted(), "note": "running agents continue; nothing new starts"}, nil
}

func resumeDispatch(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	if err := decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	var res dispatchPauseView
	if err := c.Do(ctx, http.MethodPost, "/dispatch/resume", nil, nil, &res); err != nil {
		return nil, err
	}
	return map[string]any{"dispatchPaused": res.DispatchPaused.redacted()}, nil
}

func replyInput(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a idMessage
	if err := a.check(args); err != nil {
		return nil, err
	}
	// The same marker the dashboard and the orchestrator use: an
	// input-required entry with a guardrail approval id, which the run
	// context reports as pendingApproval.
	rc, err := runContext(ctx, c, a.Identifier)
	if err != nil {
		return nil, err
	}
	if rc != nil && rc.Run != nil && rc.Run.PendingApproval {
		return nil, refuse("%s is waiting for a guardrail approval; approvals are human-only: answer it on the Itervox dashboard", a.Identifier)
	}
	if rc == nil || rc.Run == nil || rc.Run.PendingInput == "" {
		return nil, refuse("%s is not waiting for input", a.Identifier)
	}
	// fromAgent makes the event loop refuse a guardrail approval that
	// appeared after the check above, so the refusal is atomic.
	body := map[string]any{"message": a.Message, "fromAgent": true}
	if err := c.Do(ctx, http.MethodPost, issuePath(a.Identifier)+"/provide-input", nil, body, nil); err != nil {
		return nil, err
	}
	return map[string]any{"identifier": a.Identifier, "queued": true,
		"note": "the agent resumes with the reply; board_summary shows when it runs again"}, nil
}

func steer(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a idMessage
	if err := a.check(args); err != nil {
		return nil, err
	}
	var list struct {
		Sessions []struct {
			ID         string `json:"id"`
			RunID      string `json:"runId"`
			Owner      string `json:"owner"`
			Identifier string `json:"identifier"`
			Status     string `json:"status"`
			Steering   string `json:"steering"`
		} `json:"sessions"`
	}
	if err := c.Do(ctx, http.MethodGet, "/sessions", nil, nil, &list); err != nil {
		return nil, err
	}
	target, waiting := "", false
	for _, s := range list.Sessions {
		if s.Owner != "itervox" || !strings.EqualFold(s.Identifier, a.Identifier) {
			continue
		}
		if s.Status == "input_required" || s.Status == "pending_input_resume" {
			waiting = true
			continue
		}
		if s.Status == "running" && s.Steering != "" {
			target = cmp.Or(s.ID, s.RunID)
			break
		}
	}
	if target == "" {
		if waiting {
			return nil, refuse("%s is waiting for input: use reply_input", a.Identifier)
		}
		return nil, refuse("%s has no running agent that takes steering", a.Identifier)
	}
	var res struct {
		Message struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"message"`
	}
	if err := c.Do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(target)+"/message", nil,
		map[string]string{"message": a.Message}, &res); err != nil {
		return nil, err
	}
	return map[string]any{"identifier": a.Identifier, "messageId": res.Message.ID, "status": res.Message.Status}, nil
}

func remember(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a struct {
		Text  string   `json:"text"`
		Type  string   `json:"type"`
		Scope string   `json:"scope"`
		Tags  []string `json:"tags"`
		Issue string   `json:"issue"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	text := redact(strings.TrimSpace(a.Text))
	if text == "" || len(text) > maxMemoryTextBytes {
		return nil, refuse("text is required (at most %d bytes)", maxMemoryTextBytes)
	}
	scope := a.Scope
	if scope == "" {
		var view struct {
			Lessons struct {
				Scope string `json:"scope"`
			} `json:"lessons"`
		}
		if err := c.Do(ctx, http.MethodGet, "/memory", nil, nil, &view); err != nil {
			return nil, err
		}
		scope = cmp.Or(view.Lessons.Scope, "private")
	}
	entry := map[string]any{
		"type":   cmp.Or(a.Type, "fact"),
		"text":   text,
		"scope":  scope,
		"status": "proposed", // never active: a human approves it in the lessons review queue
		"source": map[string]any{"kind": mcpMemorySource, "issue": strings.TrimSpace(a.Issue)},
	}
	if len(a.Tags) > 0 {
		entry["tags"] = a.Tags
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Scope  string `json:"scope"`
	}
	if err := c.Do(ctx, http.MethodPost, "/memory", nil, entry, &created); err != nil {
		return nil, err
	}
	return map[string]any{"id": created.ID, "status": created.Status, "scope": created.Scope,
		"note": "proposed: it reaches runs only after a human approves it in Memory → Lessons"}, nil
}
