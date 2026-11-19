package jira

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// branchProperty is the issue property that stores the feature branch name.
const branchProperty = "itervox.branch"

// FetchIssueDetail returns one issue with all its comments, oldest first.
// issueID is either Jira's numeric id or an issue key such as "PROJ-12"; any
// other value is rejected with jira_invalid_issue_id before a request is made.
func (c *Client) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	if err := checkIssueRef(issueID); err != nil {
		return nil, err
	}
	path := issuePath(issueID)
	q := url.Values{"fields": {issueFieldList}, "properties": {branchProperty}}
	var raw map[string]any
	if err := c.do(ctx, "GET", path+"?"+q.Encode(), nil, &raw); err != nil {
		return nil, fmt.Errorf("jira_fetch_issue_detail: %w", err)
	}
	issue := c.normalizeIssue(raw)
	if branch := str(nested(raw, "properties", branchProperty, "branch")); branch != "" {
		issue.BranchName = &branch
	}
	comments, err := c.fetchComments(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("jira_fetch_comments: %w", err)
	}
	issue.Comments = comments
	return &issue, nil
}

// FetchIssueByIdentifier returns one issue, with comments, by its key
// (for example "PROJ-12"). A malformed key is rejected with
// jira_invalid_issue_id, and a key of another project with a
// tracker.NotFoundError, both before a request is made. An issue that Jira
// reports under another project's key (a moved issue) also gives a
// tracker.NotFoundError.
func (c *Client) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	if !issueKeyPattern.MatchString(identifier) {
		return nil, fmt.Errorf("jira_invalid_issue_id: %q is not an issue key", identifier)
	}
	if !strings.HasPrefix(identifier, c.cfg.ProjectSlug+"-") {
		return nil, &tracker.NotFoundError{Adapter: adapterName, Identifier: identifier}
	}
	issue, err := c.FetchIssueDetail(ctx, identifier)
	if err != nil {
		return nil, err
	}
	// Jira follows a moved key and answers with the issue under its new key.
	if !strings.HasPrefix(issue.Identifier, c.cfg.ProjectSlug+"-") {
		return nil, &tracker.NotFoundError{Adapter: adapterName, Identifier: identifier}
	}
	return issue, nil
}

// fetchComments loads every comment of the issue at issuePath, page by page,
// and returns them oldest first. It skips a comment id it already has and
// stops when a page adds no new comment, so a server that ignores startAt
// cannot cause duplicates. It fails after c.maxPages pages.
func (c *Client) fetchComments(ctx context.Context, issuePath string) ([]domain.Comment, error) {
	var out []domain.Comment
	seen := map[string]bool{}
	for range c.maxPages {
		q := url.Values{
			"orderBy":    {"created"},
			"startAt":    {strconv.Itoa(len(out))},
			"maxResults": {strconv.Itoa(c.commentPageSize)},
		}
		var page struct {
			Comments []map[string]any `json:"comments"`
			Total    int              `json:"total"`
		}
		if err := c.do(ctx, "GET", issuePath+"/comment?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		added := 0
		for _, raw := range page.Comments {
			cm := normalizeComment(raw)
			if seen[cm.ID] {
				continue
			}
			seen[cm.ID] = true
			out = append(out, cm)
			added++
		}
		if added == 0 || len(out) >= page.Total {
			return sortOldestFirst(out), nil
		}
	}
	return nil, fmt.Errorf("pagination did not end after %d pages", c.maxPages)
}

// sortOldestFirst orders comments by creation time. The domain contract
// requires oldest first whatever order a page used.
func sortOldestFirst(out []domain.Comment) []domain.Comment {
	slices.SortStableFunc(out, func(a, b domain.Comment) int {
		switch {
		case a.CreatedAt == nil && b.CreatedAt == nil:
			return 0
		case a.CreatedAt == nil:
			return -1
		case b.CreatedAt == nil:
			return 1
		}
		return a.CreatedAt.Compare(*b.CreatedAt)
	})
	return out
}
