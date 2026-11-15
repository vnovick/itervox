package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

func readTools() []tool {
	return []tool{
		{
			name:        "board_summary",
			description: "Board at a glance: running, retrying, paused and waiting-for-input issues, capacity, dispatch pause, failure and blocker counts, and the feed cursor.",
			scope:       scopeViewer,
			schema:      object(map[string]any{}),
			run:         boardSummary,
		},
		{
			name:        "list_issues",
			description: "List the tracker issues Itervox sees, optionally filtered by tracker state, Itervox state (idle, running, retrying, paused, input_required, pending_input_resume), label or text.",
			scope:       scopeViewer,
			schema: object(map[string]any{
				"state":             str("Tracker state, e.g. Todo (case-insensitive)"),
				"orchestratorState": str("Itervox state: idle, running, retrying, paused, input_required or pending_input_resume"),
				"label":             str("Only issues with this label (case-insensitive)"),
				"query":             str("Only issues whose identifier or title contains this text"),
				"limit":             integer("Most issues to return (default 50)", 1, maxListLimit),
			}),
			run: listIssues,
		},
		{
			name:        "get_issue",
			description: "One issue with its run context: current step, last tool call, tokens, pending question, changed files and handoffs. Never agent output.",
			scope:       scopeViewer,
			schema:      object(map[string]any{"identifier": identifierProp}, "identifier"),
			run:         getIssue,
		},
		{
			name:        "explain_blockers",
			description: "Why issues are blocked: tracker blockers from the dependency audit, inferred dependencies (confidence, whether they gate dispatch), cycles and stale blockers. Give an identifier for one issue.",
			scope:       scopeViewer,
			schema:      object(map[string]any{"identifier": identifierProp}),
			run:         explainBlockers,
		},
		{
			name:        "recent_failures",
			description: "The daemon's recent classified failures, newest first (messages redacted).",
			scope:       scopeViewer,
			schema:      object(map[string]any{"limit": integer("Most failures to return (default 20)", 1, 100)}),
			run:         recentFailures,
		},
		{
			name:        "feed_since",
			description: "Events after a cursor (dispatches, finished runs, questions, pauses, memory changes). Pass the returned nextSeq as since next time.",
			scope:       scopeViewer,
			schema: object(map[string]any{
				"since": integer("Return events with seq greater than this (default 0)", 0, 1<<53),
				"limit": integer("Most events to return (default 100)", 1, 1000),
			}),
			run: feedSince,
		},
		{
			name:        "recall",
			description: "Search Itervox memory ranked exactly like run-time retrieval (pinned first, then by relevance), within a token budget.",
			scope:       scopeViewer,
			schema: object(map[string]any{
				"query":   str("What to recall, e.g. 'staging database'"),
				"profile": str("Agent profile to recall for (default: default)"),
				"budget":  integer("Token budget (default: the run-time memory budget)", 1, 8000),
			}, "query"),
			run: recall,
		},
	}
}

func boardSummary(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	if err := decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	st, err := getState(ctx, c)
	if err != nil {
		return nil, err
	}
	running := make([]map[string]any, 0, len(st.Running))
	for _, r := range st.Running {
		row := map[string]any{"identifier": r.Identifier, "turns": r.TurnCount, "tokens": r.Tokens}
		setIf(row, "kind", r.Kind)
		setIf(row, "backend", r.Backend)
		setIf(row, "host", r.WorkerHost)
		if !r.StartedAt.IsZero() {
			row["startedAt"] = r.StartedAt
		}
		running = append(running, row)
	}
	retrying := make([]map[string]any, 0, len(st.Retrying))
	for _, r := range st.Retrying {
		row := map[string]any{"identifier": r.Identifier, "attempt": r.Attempt, "dueAt": r.DueAt}
		setIf(row, "error", clip(r.Error, maxTextBytes))
		retrying = append(retrying, row)
	}
	waiting := make([]map[string]any, 0, len(st.InputRequired))
	for _, in := range st.InputRequired {
		waiting = append(waiting, map[string]any{"identifier": in.Identifier, "state": in.State, "ageMinutes": in.AgeMinutes})
	}
	blocked := 0
	for _, a := range st.DependencyAudit {
		if len(a.UnresolvedBlockers) > 0 {
			blocked++
		}
	}
	out := map[string]any{
		"generatedAt":    st.GeneratedAt,
		"capacity":       st.MaxConcurrentAgents,
		"running":        running,
		"retrying":       retrying,
		"paused":         nonNil(st.Paused),
		"inputRequired":  waiting,
		"dispatchPaused": st.DispatchPaused.redacted(),
		"recentFailures": len(st.RecentFailures),
		"blockedIssues":  blocked,
		"feedSeq":        st.FeedSeq,
	}
	setIf(out, "project", st.ProjectName)
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func listIssues(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a struct {
		State             string `json:"state"`
		OrchestratorState string `json:"orchestratorState"`
		Label             string `json:"label"`
		Query             string `json:"query"`
		Limit             int    `json:"limit"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	var issues []issueView
	if err := c.Do(ctx, http.MethodGet, "/issues", nil, nil, &issues); err != nil {
		return nil, err
	}
	limit := limitOr(a.Limit, defaultListLimit)
	q := strings.ToLower(strings.TrimSpace(a.Query))
	rows := []map[string]any{}
	matched := 0
	for _, is := range issues {
		if a.State != "" && !strings.EqualFold(is.State, a.State) ||
			a.OrchestratorState != "" && !strings.EqualFold(is.OrchestratorState, a.OrchestratorState) ||
			a.Label != "" && !slices.ContainsFunc(is.Labels, func(l string) bool { return strings.EqualFold(l, a.Label) }) ||
			q != "" && !strings.Contains(strings.ToLower(is.Identifier+" "+is.Title), q) {
			continue
		}
		matched++
		if len(rows) < limit {
			rows = append(rows, is.compact(false))
		}
	}
	return map[string]any{"issues": rows, "matched": matched, "truncated": matched > len(rows)}, nil
}

func getIssue(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
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
	var is issueView
	if err := c.Do(ctx, http.MethodGet, issuePath(id), nil, nil, &is); err != nil {
		return nil, err
	}
	out := is.compact(true)
	rc, err := runContext(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if rc != nil {
		out["run"] = rc.compact()
	}
	return out, nil
}

func explainBlockers(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a struct {
		Identifier string `json:"identifier"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	want := strings.TrimSpace(a.Identifier)
	st, err := getState(ctx, c)
	if err != nil {
		return nil, err
	}
	match := func(ids ...string) bool {
		return want == "" || slices.ContainsFunc(ids, func(id string) bool { return strings.EqualFold(id, want) })
	}
	tracker := []map[string]any{}
	for _, d := range st.DependencyAudit {
		if len(d.UnresolvedBlockers) == 0 || !match(d.Identifier) {
			continue
		}
		blockers := make([]string, 0, len(d.UnresolvedBlockers))
		for _, b := range d.UnresolvedBlockers {
			blockers = append(blockers, strings.TrimSpace(b.Identifier+" ("+b.State+")"))
		}
		row := map[string]any{"identifier": d.Identifier, "state": d.IssueState, "blockedBy": blockers}
		if d.FirstBlockedAt != nil {
			row["blockedSince"] = *d.FirstBlockedAt
		}
		if d.Degraded {
			row["degraded"] = true
		}
		tracker = append(tracker, row)
	}
	inferred := []map[string]any{}
	for _, e := range st.DependencyGraphEdges {
		if e.Origin != "inferred" || e.Resolved || !match(e.SourceIdentifier, e.TargetIdentifier) {
			continue
		}
		row := map[string]any{"blocked": e.TargetIdentifier, "blocker": e.SourceIdentifier,
			"confidence": e.Confidence, "gatesDispatch": e.Gating}
		setIf(row, "blockerState", e.SourceState)
		setIf(row, "evidence", clip(e.Evidence, maxTextBytes))
		if e.Stale {
			row["stale"] = true
		}
		if e.Overridden {
			row["overridden"] = true
		}
		inferred = append(inferred, row)
	}
	cycles := [][]string{}
	for _, cy := range st.DependencyCycles {
		if match(cy.Members...) {
			cycles = append(cycles, cy.Members)
		}
	}
	attention := []map[string]any{}
	for _, at := range st.DependencyAttention {
		if match(at.Identifier) {
			attention = append(attention, map[string]any{"identifier": at.Identifier, "kind": at.Kind,
				"blockers": at.Blockers, "blockedSince": at.BlockedSince})
		}
	}
	return map[string]any{"trackerBlockers": tracker, "inferredBlockers": inferred, "cycles": cycles, "attention": attention}, nil
}

func recentFailures(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a struct {
		Limit int `json:"limit"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	st, err := getState(ctx, c)
	if err != nil {
		return nil, err
	}
	limit := min(limitOr(a.Limit, 20), 100)
	rows := []map[string]any{}
	for _, f := range st.RecentFailures {
		if len(rows) == limit {
			break
		}
		row := map[string]any{"kind": f.Kind, "message": clip(f.Message, maxTextBytes), "at": f.OccurredAt, "count": f.Count}
		setIf(row, "identifier", f.Identifier)
		setIf(row, "source", f.Source)
		rows = append(rows, row)
	}
	return map[string]any{"failures": rows, "total": len(st.RecentFailures)}, nil
}

func feedSince(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a struct {
		Since int64 `json:"since"`
		Limit int   `json:"limit"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if a.Since < 0 || a.Limit < 0 || a.Limit > 1000 {
		return nil, refuse("since must be ≥ 0 and limit between 1 and 1000")
	}
	q := url.Values{"since": {strconv.FormatInt(a.Since, 10)}}
	if a.Limit > 0 {
		q.Set("limit", strconv.Itoa(a.Limit))
	}
	var page struct {
		Events []struct {
			Seq        int64           `json:"seq"`
			TS         time.Time       `json:"ts"`
			Kind       string          `json:"kind"`
			Identifier string          `json:"identifier,omitempty"`
			Payload    json.RawMessage `json:"payload,omitempty"`
		} `json:"events"`
		NextSeq   int64 `json:"nextSeq"`
		HeadSeq   int64 `json:"headSeq"`
		Truncated bool  `json:"truncated"`
		HasMore   bool  `json:"hasMore"`
	}
	if err := c.Do(ctx, http.MethodGet, "/feed", q, nil, &page); err != nil {
		return nil, err
	}
	events := make([]map[string]any, 0, len(page.Events))
	for _, e := range page.Events {
		row := map[string]any{"seq": e.Seq, "ts": e.TS, "kind": e.Kind}
		setIf(row, "identifier", e.Identifier)
		// The feed payload is small and redacted by the daemon (no agent
		// output, no prompt text); its free text is redacted again here.
		var p map[string]any
		if len(e.Payload) > 0 && json.Unmarshal(e.Payload, &p) == nil && len(p) > 0 {
			for k, v := range p {
				if s, ok := v.(string); ok {
					p[k] = clip(s, maxTextBytes)
				}
			}
			row["payload"] = p
		}
		events = append(events, row)
	}
	return map[string]any{"events": events, "nextSeq": page.NextSeq, "headSeq": page.HeadSeq,
		"truncated": page.Truncated, "hasMore": page.HasMore}, nil
}

func recall(ctx context.Context, c *Client, args json.RawMessage) (any, error) {
	var a struct {
		Query   string `json:"query"`
		Profile string `json:"profile"`
		Budget  int    `json:"budget"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Query) == "" {
		return nil, refuse("query is required")
	}
	q := url.Values{"q": {a.Query}}
	if a.Profile != "" {
		q.Set("profile", a.Profile)
	}
	if a.Budget > 0 {
		q.Set("budget", strconv.Itoa(a.Budget))
	}
	var v struct {
		Enabled bool   `json:"enabled"`
		Profile string `json:"profile"`
		Budget  int    `json:"budget"`
		Tokens  int    `json:"tokens"`
		Dropped int    `json:"dropped"`
		Entries []struct {
			ID     string   `json:"id"`
			Type   string   `json:"type"`
			Text   string   `json:"text"`
			Scope  string   `json:"scope"`
			Tags   []string `json:"tags"`
			Pinned bool     `json:"pinned"`
		} `json:"entries"`
		OverBudget bool `json:"overBudget"`
	}
	if err := c.Do(ctx, http.MethodGet, "/memory/recall", q, nil, &v); err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(v.Entries))
	for _, e := range v.Entries {
		row := map[string]any{"id": e.ID, "type": e.Type, "scope": e.Scope, "text": redact(e.Text)}
		setIf(row, "tags", e.Tags)
		if e.Pinned {
			row["pinned"] = true
		}
		entries = append(entries, row)
	}
	out := map[string]any{"entries": entries, "profile": v.Profile, "budget": v.Budget, "tokens": v.Tokens, "dropped": v.Dropped}
	if !v.Enabled {
		out["note"] = "memory.enabled is off: runs get no memory block"
	}
	if v.OverBudget {
		out["overBudget"] = true
	}
	return out, nil
}
