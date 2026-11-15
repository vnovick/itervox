package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// Issue editing for the local issue sync (#118). GitHub keeps an issue's
// state and priority in labels and its blockers in the body, so:
//
//   - a labels patch replaces the issue's other labels and keeps its state
//     labels (the configured active, terminal, backlog and completion states)
//     and its pN priority label;
//   - a priority patch replaces the pN label (-1 removes it);
//   - a blockers patch rewrites one managed body line, "Blocked by #3, #4"
//     ending in a hidden marker, which the body parser reads like any other
//     "Blocked by" sentence; a description patch keeps that line.

var _ tracker.IssueEditor = (*Client)(nil)

// CreateScopedIssue implements tracker.IssueEditor: it creates the issue in
// the configured repository. GitHub accepts no client-supplied id, so
// draft.Key is not sent (see FindIssueByKey).
func (c *Client) CreateScopedIssue(ctx context.Context, draft tracker.IssueDraft) (*domain.Issue, error) {
	labels := []string{}
	if s := strings.TrimSpace(draft.State); s != "" {
		labels = append(labels, s)
	}
	labels = append(labels, draft.Labels...)
	if draft.Priority != nil && *draft.Priority >= 0 {
		labels = append(labels, "p"+strconv.Itoa(*draft.Priority))
	}
	body := tracker.JoinSyncBlockersLine(draft.Description, tracker.SyncBlockersLine(issueRefs(draft.BlockedBy)))
	raw, err := c.sendJSON(ctx, http.MethodPost, c.issuesURL(""), map[string]any{
		"title": draft.Title, "body": body, "labels": labels,
	}, http.StatusCreated)
	if err != nil {
		return nil, fmt.Errorf("github_create_scoped_issue: %w", err)
	}
	derived := deriveState(raw, c.activeStates(), c.terminalStates())
	if derived == "" {
		derived = draft.State
	}
	issue := normalizeIssue(raw, derived)
	if issue == nil {
		return nil, fmt.Errorf("github_create_scoped_issue: missing issue fields in response")
	}
	return issue, nil
}

// FindIssueByKey implements tracker.IssueEditor. GitHub cannot look an issue
// up by a client key, so it always answers "not found": a create whose
// response was lost is re-sent and may create a duplicate issue (the same
// residual the outbox documents for GitHub comments).
func (c *Client) FindIssueByKey(context.Context, string) (*domain.Issue, bool, error) {
	return nil, false, nil
}

// UpdateIssueFields implements tracker.IssueEditor.
func (c *Client) UpdateIssueFields(ctx context.Context, issueID string, patch tracker.IssueFieldsPatch) error {
	if patch.Empty() {
		return nil
	}
	var current map[string]any
	if patch.Description != nil || patch.BlockedBy != nil || patch.Labels != nil || patch.Priority != nil {
		res, _, err := c.get(ctx, c.issuesURL(issueID))
		if err != nil {
			return fmt.Errorf("github_update_fields: read issue: %w", err)
		}
		current, _ = res.(map[string]any)
		if current == nil {
			return fmt.Errorf("github_update_fields: unexpected issue shape")
		}
	}
	edit := map[string]any{}
	if patch.Title != nil {
		edit["title"] = *patch.Title
	}
	if patch.Description != nil || patch.BlockedBy != nil {
		body, _ := current["body"].(string)
		desc, line := tracker.SplitSyncBlockersLine(body)
		if patch.Description != nil {
			desc = *patch.Description
		}
		if patch.BlockedBy != nil {
			line = tracker.SyncBlockersLine(issueRefs(*patch.BlockedBy))
		}
		edit["body"] = tracker.JoinSyncBlockersLine(desc, line)
	}
	if patch.Labels != nil || patch.Priority != nil {
		edit["labels"] = c.mergeLabels(extractLabelNames(current), patch)
	}
	if len(edit) == 0 {
		return nil
	}
	if _, err := c.sendJSON(ctx, http.MethodPatch, c.issuesURL(issueID), edit, http.StatusOK); err != nil {
		return fmt.Errorf("github_update_fields: %w", err)
	}
	return nil
}

// mergeLabels builds the issue's new label set: state labels always kept,
// the priority label kept unless the patch sets one, the rest kept unless
// the patch replaces them.
func (c *Client) mergeLabels(current []string, patch tracker.IssueFieldsPatch) []string {
	stateLabels := append(append(append([]string{}, c.activeStates()...), c.terminalStates()...), c.cfg.BacklogStates...)
	if cs := strings.TrimSpace(c.cfg.CompletionState); cs != "" {
		stateLabels = append(stateLabels, cs)
	}
	isState := func(l string) bool {
		return slices.ContainsFunc(stateLabels, func(s string) bool { return strings.EqualFold(s, l) })
	}
	var out []string
	add := func(l string) {
		if l != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, l) }) {
			out = append(out, l)
		}
	}
	for _, l := range current {
		switch {
		case isState(l):
			add(l)
		case isPriorityLabel(l):
			if patch.Priority == nil {
				add(l)
			}
		case patch.Labels == nil:
			add(l)
		}
	}
	if patch.Labels != nil {
		for _, l := range *patch.Labels {
			if !isState(l) && !isPriorityLabel(l) {
				add(l)
			}
		}
	}
	if patch.Priority != nil && *patch.Priority >= 0 {
		add("p" + strconv.Itoa(*patch.Priority))
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// isPriorityLabel reports whether l is a pN label priorityFromLabels reads.
func isPriorityLabel(l string) bool {
	return priorityFromLabels([]string{strings.ToLower(l)}) >= 0
}

// extractLabelNames returns the label names as GitHub spells them.
func extractLabelNames(raw map[string]any) []string {
	items, _ := raw["labels"].([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			if n, _ := m["name"].(string); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// issueRefs turns issue numbers into "#N" references.
func issueRefs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "#"+strings.TrimPrefix(id, "#"))
	}
	return out
}

// issuesURL is the repository's issues collection, or one issue.
func (c *Client) issuesURL(number string) string {
	u := fmt.Sprintf("%s/repos/%s/%s/issues", c.cfg.Endpoint, c.owner, c.repo)
	if number != "" {
		u += "/" + number
	}
	return u
}

// sendJSON sends payload and decodes the JSON object in the response,
// failing on any status but want.
func (c *Client) sendJSON(ctx context.Context, method, u string, payload any, want int) (map[string]any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := tracker.DoWithRateLimitRetry(ctx, c.httpClient, req, "github", tracker.GitHubRateLimitClassifier)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	c.snapshotRateLimit(resp)
	if resp.StatusCode != want {
		return nil, &tracker.APIStatusError{Adapter: "github", Status: resp.StatusCode}
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode body: %w", err)
	}
	return raw, nil
}
