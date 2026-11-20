package jira

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
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
// and returns the stored comment. Markdown in the text is sent as Jira rich
// text that reads back as the same text (see textToADF and postText).
// Itervox's markers at the end of body go in comment properties, not in the
// text (see splitMarkers), so Jira never shows them and nobody can copy
// them; the returned comment, like every read, has them back in place (see
// normalizeComment). A text longer than Jira's content limit is shortened
// first (fitContentLimit), and the returned comment holds the shortened
// text. A body that is blank is refused, and an invalid issue id is
// refused, both before any request.
func (c *Client) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	return c.createComment(ctx, "jira_create_comment", issueID, body, "")
}

// CreateCommentWithKey implements tracker.IdempotentCommenter. It posts body
// like CreateComment and sends key in the same request, as the comment
// property itervox.commentKey, so no second request has to attach the key.
// The text people read in Jira stays as typed. FindCommentByKey and
// FetchIssueDetail read the key back. A blank key is refused before any
// request.
func (c *Client) CreateCommentWithKey(ctx context.Context, issueID, key, body string) (*domain.Comment, error) {
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("jira_create_comment_with_key: empty key")
	}
	return c.createComment(ctx, "jira_create_comment_with_key", issueID, body, key)
}

// createComment posts body on the issue, with key in the comment key
// property when key is not empty. op names the operation in errors.
func (c *Client) createComment(ctx context.Context, op, issueID, body, key string) (*domain.Comment, error) {
	if err := checkIssueRef(issueID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("%s: empty body", op)
	}
	text, managed, textKey := splitMarkers(body, key)
	// The markers are not stored as text, so only the text counts for the limit.
	text = fitContentLimit(text)
	if key == "" {
		key = textKey
	}
	var props []any
	if managed {
		props = append(props, map[string]any{"key": managedProperty, "value": map[string]any{"managed": true}})
	}
	if key != "" {
		props = append(props, map[string]any{"key": commentKeyProperty, "value": map[string]any{"key": key}})
	}
	// The properties travel in the same request as the text, so the plain
	// retry of postText carries them too.
	withDoc := func(doc map[string]any) any {
		req := map[string]any{"body": doc}
		if len(props) > 0 {
			req["properties"] = props
		}
		return req
	}
	var raw map[string]any
	if err := c.postText(ctx, issuePath(issueID)+"/comment", text, withDoc, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	// The create response carries no properties. Read with the properties
	// that were sent, it gives the body every later read gives.
	if raw == nil {
		raw = map[string]any{}
	}
	if raw["body"] == nil {
		raw["body"] = text
	}
	raw["properties"] = props
	cm := normalizeComment(raw)
	return &cm, nil
}

// splitMarkers takes Itervox's markers off the end of body, where
// tracker.MarkManagedComment and tracker.MarkCommentKey put them (a blank
// line, then the marker), in either order. It returns the text before them,
// whether the body is managed, and the key of the key marker, or "". One key
// marker is taken, and only when wantKey is "" or its key. Any other marker
// stays in the text, and so does a marker that is the whole text. A body
// that holds the managed marker anywhere else is managed too, because
// tracker.MarkManagedComment adds no second marker to it.
func splitMarkers(body, wantKey string) (text string, managed bool, key string) {
	text = strings.TrimRight(body, " \t\r\n")
	for {
		i := strings.LastIndex(text, "\n\n")
		if i < 0 || strings.TrimSpace(text[:i]) == "" {
			break
		}
		last := text[i+2:]
		if k, ok := keyOfMarker(last); ok && key == "" && (wantKey == "" || k == wantKey) {
			key = k
		} else if last == tracker.ManagedCommentMarker {
			managed = true
		} else {
			break
		}
		text = text[:i]
	}
	return text, managed || strings.Contains(text, tracker.ManagedCommentMarker), key
}

// keyOfMarker returns the key when s is exactly tracker.CommentKeyMarker of
// a key that is not blank.
func keyOfMarker(s string) (string, bool) {
	rest, hasPrefix := strings.CutPrefix(s, markerPrefix+"ck:")
	key, hasSuffix := strings.CutSuffix(rest, " -->")
	return key, hasPrefix && hasSuffix && strings.TrimSpace(key) != ""
}

// postText posts a request that carries text as an ADF document; withDoc
// returns the request with the document in it. If Jira refuses the rich
// document as invalid (400), the text is sent once more as plain paragraphs,
// as the adapter sent all text before Markdown support, so that formatting
// can never cost a comment or an issue. Text that has no rich part is not
// sent twice.
func (c *Client) postText(ctx context.Context, path, text string, withDoc func(doc map[string]any) any, out any) error {
	rich := textToADF(text)
	err := c.do(ctx, "POST", path, withDoc(rich), out)
	var status *tracker.APIStatusError
	if errors.As(err, &status) && status.Status == http.StatusBadRequest {
		if plain := plainTextToADF(text); !reflect.DeepEqual(plain, rich) {
			err = c.do(ctx, "POST", path, withDoc(plain), out)
		}
	}
	return err
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
// source, it is a "Task". The body is the description, sent like a comment
// (see postText). Labels are trimmed and de-duplicated without case.
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
	withDescription := func(doc map[string]any) any {
		if strings.TrimSpace(in.Body) != "" {
			fields["description"] = doc
		}
		return req
	}
	if err := c.postText(ctx, "/rest/api/3/issue", fitContentLimit(in.Body), withDescription, &created); err != nil {
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
