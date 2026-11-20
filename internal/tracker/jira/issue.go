package jira

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

const (
	// branchProperty is the issue property that stores the feature branch name.
	branchProperty = "itervox.branch"
	// commentKeyProperty is the comment property that stores a comment's
	// idempotency key, as {"key": "<key>"}. It stands for
	// tracker.CommentKeyMarker.
	commentKeyProperty = "itervox.commentKey"
	// managedProperty is the comment property that marks a comment Itervox
	// wrote, as {"managed": true}. It stands for tracker.ManagedCommentMarker,
	// which Jira would show as text.
	managedProperty = "itervox.managed"
)

// FetchIssueDetail returns one issue with all its comments, oldest first.
// issueID is either Jira's numeric id or an issue key such as "PROJ-12"; any
// other value is rejected with jira_invalid_issue_id before a request is made.
// It fails when the properties of a comment cannot be read, because
// Itervox's markers are in them (see addCommentProperties).
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
	raws, _, err := c.fetchComments(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("jira_fetch_comments: %w", err)
	}
	issue.Comments = normalizeComments(raws)
	return &issue, nil
}

// FindCommentByKey implements tracker.IdempotentCommenter. It reads the
// issue's comments with their properties and returns the comment whose
// itervox.commentKey property holds key; a key marker in a comment's text
// does not count. It returns (nil, false, nil) only when every page was
// read, the properties of every comment were read, and no comment holds key.
// Anything less is an error, because the caller treats "not found" as
// permission to post again: a failed request, a read that ends before the
// total Jira reports, or a comment whose properties could not be read. An
// invalid issue id and a blank key are refused before any request.
//
// Residual: Jira pages comments by offset. If an older comment is deleted
// between two page reads, the comments after it move back one place, and the
// one at the page boundary is skipped. This needs more than one page of
// comments (100) and a delete during the lookup.
func (c *Client) FindCommentByKey(ctx context.Context, issueID, key string) (*domain.Comment, bool, error) {
	if err := checkIssueRef(issueID); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, false, errors.New("jira_find_comment_by_key: empty key")
	}
	raws, complete, err := c.fetchComments(ctx, issuePath(issueID))
	if err != nil {
		return nil, false, fmt.Errorf("jira_find_comment_by_key: %w", err)
	}
	var keyed []map[string]any
	for _, raw := range raws {
		if commentKeyOf(raw) == key {
			keyed = append(keyed, raw)
		}
	}
	if found := normalizeComments(keyed); len(found) > 0 {
		return &found[0], true, nil
	}
	if !complete {
		return nil, false, errors.New("jira_find_comment_by_key: the comment pages ended before the reported total, so the answer is unknown")
	}
	return nil, false, nil
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

// fetchComments loads every raw comment of the issue at issuePath with its
// properties. complete is false when the comment pages ended before the
// total Jira reported (see commentPages): comments can then be missing. A
// failed request is an error, and so is a comment whose properties could not
// be read (see addCommentProperties).
func (c *Client) fetchComments(ctx context.Context, issuePath string) (raws []map[string]any, complete bool, err error) {
	raws, complete, err = c.commentPages(ctx, issuePath)
	if err != nil {
		return nil, false, err
	}
	if err := c.addCommentProperties(ctx, raws); err != nil {
		return nil, false, err
	}
	return raws, complete, nil
}

// normalizeComments maps raw comments to domain comments, oldest first.
func normalizeComments(raws []map[string]any) []domain.Comment {
	var out []domain.Comment
	for _, raw := range raws {
		out = append(out, normalizeComment(raw))
	}
	return sortOldestFirst(out)
}

// commentPages loads every raw comment of the issue at issuePath, page by
// page. It skips a comment id it already has and stops when a page adds no
// new comment, so a server that ignores startAt cannot cause duplicates.
// complete is false when it stopped before the total Jira reported, or a page
// had no total. It fails after c.maxPages pages.
func (c *Client) commentPages(ctx context.Context, issuePath string) (raws []map[string]any, complete bool, err error) {
	seen := map[string]bool{}
	for range c.maxPages {
		q := url.Values{
			"orderBy":    {"created"},
			"startAt":    {strconv.Itoa(len(raws))},
			"maxResults": {strconv.Itoa(c.commentPageSize)},
		}
		var page struct {
			Comments []map[string]any `json:"comments"`
			Total    *int             `json:"total"`
		}
		if err := c.do(ctx, "GET", issuePath+"/comment?"+q.Encode(), nil, &page); err != nil {
			return nil, false, err
		}
		added := 0
		for _, raw := range page.Comments {
			if id := str(raw["id"]); !seen[id] {
				seen[id] = true
				raws = append(raws, raw)
				added++
			}
		}
		if page.Total == nil {
			return raws, false, nil
		}
		if added == 0 || len(raws) >= *page.Total {
			return raws, len(raws) >= *page.Total, nil
		}
	}
	return nil, false, fmt.Errorf("pagination did not end after %d pages", c.maxPages)
}

// commentListChunk is the most comment ids one comment/list request may
// carry; Atlassian documents a maximum of 1000.
const commentListChunk = 1000

// addCommentProperties reads the properties of raws through
// POST /rest/api/3/comment/list?expand=properties, the documented way to read
// comment properties in bulk, in chunks of commentListChunk ids, and stores
// each comment's list under "properties". It sends no request when raws is
// empty. It fails when a request fails, and when a comment has no numeric
// id, is missing from the response (Jira leaves out ids it cannot return),
// or comes back without a properties list. Such a comment could be one of
// Itervox's own comments with its markers unknown: read without them, it
// would pass for a person's comment, and an agent waiting for a reply would
// resume on Itervox's own text. The caller reads again on its next poll.
func (c *Client) addCommentProperties(ctx context.Context, raws []map[string]any) error {
	ctx = tracker.WithReadIntent(ctx) // a POST that only reads
	for start := 0; start < len(raws); start += commentListChunk {
		chunk := raws[start:min(start+commentListChunk, len(raws))]
		ids := make([]int64, 0, len(chunk))
		for _, raw := range chunk {
			// A comment without a numeric id is not asked for, so it is
			// missing from the response below and the read fails.
			if id, perr := strconv.ParseInt(str(raw["id"]), 10, 64); perr == nil {
				ids = append(ids, id)
			}
		}
		var resp struct {
			Values []map[string]any `json:"values"`
		}
		if err := c.do(ctx, "POST", "/rest/api/3/comment/list?expand=properties", map[string]any{"ids": ids}, &resp); err != nil {
			return fmt.Errorf("read comment properties: %w", err)
		}
		props := map[string][]any{}
		for _, v := range resp.Values {
			if list, isList := v["properties"].([]any); isList {
				props[str(v["id"])] = list
			}
		}
		for _, raw := range chunk {
			list, found := props[str(raw["id"])]
			if !found {
				return fmt.Errorf("read comment properties: none for comment %q", str(raw["id"]))
			}
			raw["properties"] = list
		}
	}
	return nil
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
