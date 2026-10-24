package prwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// review.go — #238: the write side of the GitHub client, for change review
// from the dashboard: a pull request's files (base...head, as GitHub will
// merge them), a single line or range review comment, and one review
// (approve / request changes / comment) carrying batched line comments.
// Same Client, same token source and same API root as the PR-event watcher:
// there is no second GitHub credential path. Nothing here merges.

// PRFile is one changed file of a pull request.
type PRFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename,omitempty"`
	// Status is added, removed, modified, renamed, copied, changed or
	// unchanged.
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Patch is the file's unified diff hunks; GitHub omits it for binary
	// files and very large diffs.
	Patch string `json:"patch,omitempty"`
}

// ReviewSide is the side of a diff a comment anchors to.
const (
	SideRight = "RIGHT" // the head (new) file
	SideLeft  = "LEFT"  // the base (old) file
)

// Review events.
const (
	ReviewApprove        = "APPROVE"
	ReviewRequestChanges = "REQUEST_CHANGES"
	ReviewComment        = "COMMENT"
)

// LineComment is one review comment on a file line or line range. Line is
// the range's last line; StartLine (0 for a single line) its first.
type LineComment struct {
	Path      string `json:"path"`
	Body      string `json:"body"`
	Line      int    `json:"line"`
	Side      string `json:"side,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
}

// NewReview is one pull request review.
type NewReview struct {
	// CommitID is the head SHA the review applies to.
	CommitID string        `json:"commit_id,omitempty"`
	Body     string        `json:"body,omitempty"`
	Event    string        `json:"event"`
	Comments []LineComment `json:"comments,omitempty"`
}

// Created is what GitHub answers for a created comment or review.
type Created struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
}

// APIError is a GitHub refusal (4xx/5xx other than 404). Message is
// GitHub's own message (for example "Can not approve your own pull
// request"); it never carries the request body or the token.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("prwatch: GitHub answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("prwatch: GitHub answered HTTP %d: %s", e.Status, e.Message)
}

// maxPRFiles bounds a file listing (GitHub itself stops at 3000).
const maxPRFiles = 3000

// PRFiles lists the pull request's changed files (GitHub's base...head
// comparison, which is what a merge applies).
func (c *Client) PRFiles(ctx context.Context, ref PRRef) ([]PRFile, error) {
	var all []PRFile
	next := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100", ref.Owner, ref.Repo, ref.Number)
	for page := 0; next != "" && page < maxPRFiles/100; page++ {
		var body []PRFile
		var err error
		if next, err = c.get(ctx, ref.Host, next, &body); err != nil {
			return nil, err
		}
		all = append(all, body...)
	}
	return all, nil
}

// CreateReviewComment posts one line or range comment on commitID.
func (c *Client) CreateReviewComment(ctx context.Context, ref PRRef, commitID string, lc LineComment) (Created, error) {
	in := struct {
		LineComment
		CommitID string `json:"commit_id"`
	}{LineComment: lc, CommitID: commitID}
	var out Created
	err := c.post(ctx, ref.Host, fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", ref.Owner, ref.Repo, ref.Number), in, &out)
	return out, err
}

// CreateReview submits one review with its line comments in a single
// request: GitHub records them together, as one review.
func (c *Client) CreateReview(ctx context.Context, ref PRRef, r NewReview) (Created, error) {
	var out Created
	err := c.post(ctx, ref.Host, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", ref.Owner, ref.Repo, ref.Number), r, &out)
	return out, err
}

// post POSTs in as JSON to path and decodes the 2xx answer into out.
func (c *Client) post(ctx context.Context, host, path string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("prwatch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL(host)+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("prwatch: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	if c.Token != nil {
		if tok := c.Token(host); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("prwatch: POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.noteRateLimit(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("prwatch: POST %s: %w", path, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		var ge struct {
			Message string `json:"message"`
			Errors  []any  `json:"errors"`
		}
		_ = json.Unmarshal(body, &ge)
		msg := ge.Message
		if len(ge.Errors) > 0 {
			if detail, err := json.Marshal(ge.Errors); err == nil && len(detail) <= 300 {
				msg += " " + string(detail)
			}
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("prwatch: POST %s: decode: %w", path, err)
	}
	return nil
}

// IsAPIError reports whether err is a GitHub refusal and returns it.
func IsAPIError(err error) (*APIError, bool) {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}
