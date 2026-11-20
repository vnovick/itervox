package jira

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// checkIssueRef returns a jira_invalid_issue_id error unless id is a numeric
// issue id or an issue key.
func checkIssueRef(id string) error {
	if !issueIDPattern.MatchString(id) && !issueKeyPattern.MatchString(id) {
		return fmt.Errorf("jira_invalid_issue_id: %q is neither a numeric id nor an issue key", id)
	}
	return nil
}

// issuePath returns the REST path of one issue. id must pass checkIssueRef.
func issuePath(id string) string { return "/rest/api/3/issue/" + url.PathEscape(id) }

// CreateComment posts body as a comment on the issue (a numeric id or a key)
// and returns the stored comment. The text is sent as one ADF paragraph per
// line, so Markdown is kept as typed. A body longer than Jira's content limit
// is shortened first (fitContentLimit), and the returned comment holds the
// shortened text. A body that is blank is refused, and an invalid issue id is
// refused, both before any request.
func (c *Client) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	if err := checkIssueRef(issueID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("jira_create_comment: empty body")
	}
	body = fitContentLimit(body)
	var raw map[string]any
	if err := c.do(ctx, "POST", issuePath(issueID)+"/comment", map[string]any{"body": textToADF(body)}, &raw); err != nil {
		return nil, fmt.Errorf("jira_create_comment: %w", err)
	}
	cm := normalizeComment(raw)
	if cm.Body == "" {
		cm.Body = body
	}
	return &cm, nil
}

// UpdateIssueState moves the issue to the status named stateName (compared
// without case). It reads the status of the issue first; when the issue
// already has the wanted status, it returns nil and sends nothing more.
// Otherwise it runs the first available transition that ends in that status,
// or returns an error naming the current status and the statuses the issue
// can reach.
func (c *Client) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	if err := checkIssueRef(issueID); err != nil {
		return err
	}
	path := issuePath(issueID)
	var cur map[string]any
	if err := c.do(ctx, "GET", path+"?fields=status", nil, &cur); err != nil {
		return fmt.Errorf("jira_update_state: %w", err)
	}
	current := str(nested(cur, "fields", "status", "name"))
	if strings.EqualFold(current, stateName) {
		return nil
	}
	var list struct {
		Transitions []struct {
			ID string `json:"id"`
			To struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := c.do(ctx, "GET", path+"/transitions", nil, &list); err != nil {
		return fmt.Errorf("jira_update_state: %w", err)
	}
	reachable := make([]string, 0, len(list.Transitions))
	for _, t := range list.Transitions {
		if strings.EqualFold(t.To.Name, stateName) {
			body := map[string]any{"transition": map[string]any{"id": t.ID}}
			if err := c.do(ctx, "POST", path+"/transitions", body, nil); err != nil {
				return fmt.Errorf("jira_update_state: %w", err)
			}
			return nil
		}
		reachable = append(reachable, t.To.Name)
	}
	slices.Sort(reachable)
	return fmt.Errorf("jira_update_state: no transition to %q from %q (reachable: %s)",
		stateName, current, strings.Join(reachable, ", "))
}

// SetIssueBranch stores branchName in the issue property that
// FetchIssueDetail reads back as the issue's branch.
func (c *Client) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	if err := checkIssueRef(issueID); err != nil {
		return err
	}
	path := issuePath(issueID) + "/properties/" + url.PathEscape(branchProperty)
	if err := c.do(ctx, "PUT", path, map[string]any{"branch": branchName}, nil); err != nil {
		return fmt.Errorf("jira_set_branch: %w", err)
	}
	return nil
}

// CreateIssue creates a follow-up issue in the configured project and returns
// it as Jira now stores it. With a source issue, the new issue copies the
// source's issue type, unless that type is a sub-task; then, and with no
// source, it is a "Task". Labels are trimmed and de-duplicated without case.
// When in.BlockedBySource is set, the source is linked as the blocker inside
// the create request, so a rejected create leaves nothing behind. When
// in.State is set, the issue is moved there; if the move fails, the new issue
// is deleted so that no poll can dispatch it, and the error says so. If the
// delete fails too, the error names the key. A failed read-back after a
// successful move keeps the issue, which is in its state with its blocker, and
// names the key. The returned issue is nil in every error case.
func (c *Client) CreateIssue(ctx context.Context, sourceIssueID string, in tracker.NewIssue) (*domain.Issue, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, errors.New("jira_create_issue: empty title")
	}
	if sourceIssueID != "" {
		if err := checkIssueRef(sourceIssueID); err != nil {
			return nil, err
		}
	}
	issuetype := map[string]any{"name": "Task"}
	if sourceIssueID != "" {
		var src map[string]any
		if err := c.do(ctx, "GET", issuePath(sourceIssueID)+"?fields=issuetype", nil, &src); err != nil {
			return nil, fmt.Errorf("jira_create_issue: read source issue type: %w", err)
		}
		sub, _ := nested(src, "fields", "issuetype", "subtask").(bool)
		if id := str(nested(src, "fields", "issuetype", "id")); id != "" && !sub {
			issuetype = map[string]any{"id": id}
		}
	}
	fields := map[string]any{
		"project":   map[string]any{"key": c.cfg.ProjectSlug},
		"summary":   in.Title,
		"issuetype": issuetype,
	}
	if strings.TrimSpace(in.Body) != "" {
		fields["description"] = textToADF(fitContentLimit(in.Body))
	}
	if labels := cleanLabels(in.Labels); len(labels) > 0 {
		fields["labels"] = labels
	}
	req := map[string]any{"fields": fields}
	if in.BlockedBySource && sourceIssueID != "" {
		inward := map[string]any{"key": sourceIssueID}
		if issueIDPattern.MatchString(sourceIssueID) {
			inward = map[string]any{"id": sourceIssueID}
		}
		req["update"] = map[string]any{"issuelinks": []any{map[string]any{"add": map[string]any{
			"type":        map[string]any{"name": "Blocks"},
			"inwardIssue": inward,
		}}}}
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := c.do(ctx, "POST", "/rest/api/3/issue", req, &created); err != nil {
		return nil, fmt.Errorf("jira_create_issue: %w", err)
	}
	if strings.TrimSpace(in.State) != "" {
		if err := c.UpdateIssueState(ctx, created.ID, in.State); err != nil {
			// The caller's context may be the reason the move failed, so the
			// delete must not inherit its cancellation.
			if delErr := c.do(context.WithoutCancel(ctx), "DELETE", issuePath(created.ID), nil, nil); delErr != nil {
				return nil, fmt.Errorf("jira_create_issue: created %s but could not move it to %q and could not delete it: %w; delete: %w",
					created.Key, in.State, err, delErr)
			}
			return nil, fmt.Errorf("jira_create_issue: could not move %s to %q; the new issue was deleted: %w", created.Key, in.State, err)
		}
	}
	issues, err := c.FetchIssueStatesByIDs(ctx, []string{created.ID})
	if err != nil {
		return nil, fmt.Errorf("jira_create_issue: created %s but could not read it back: %w", created.Key, err)
	}
	if len(issues) == 0 {
		return nil, fmt.Errorf("jira_create_issue: created %s but could not read it back: issue not returned", created.Key)
	}
	return &issues[0], nil
}

// cleanLabels trims each label, drops empty ones, and drops repeats that
// differ only in case. It keeps the first spelling and the order.
func cleanLabels(labels []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" || seen[strings.ToLower(l)] {
			continue
		}
		seen[strings.ToLower(l)] = true
		out = append(out, l)
	}
	return out
}
