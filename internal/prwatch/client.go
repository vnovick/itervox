// Package prwatch watches itervox-managed GitHub pull requests for the
// PR-event automation triggers (#173): completed check runs and commit
// statuses, new pushes, submitted reviews and mergeable_state changes. It
// learns about them from signed GitHub webhook deliveries (Handler) and from
// a poll of the open managed PRs (Watcher.Poll), and hands each observation
// to an Emit function. It decides nothing about dispatch: the orchestrator's
// event loop dedups, checks that the PR is managed, and runs automations.
package prwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/tracker"
)

// PRRef identifies a pull request.
type PRRef struct {
	Host   string // github.com, or a GitHub Enterprise host
	Owner  string
	Repo   string
	Number int
}

var prURLRe = regexp.MustCompile(`^https://([^/\s]+)/([^/\s]+)/([^/\s]+)/pull/(\d+)/?$`)

// ParsePRURL parses https://<host>/<owner>/<repo>/pull/<n>.
func ParsePRURL(u string) (PRRef, bool) {
	m := prURLRe.FindStringSubmatch(strings.TrimSpace(u))
	if m == nil {
		return PRRef{}, false
	}
	n, err := strconv.Atoi(m[4])
	if err != nil || n <= 0 {
		return PRRef{}, false
	}
	return PRRef{Host: strings.ToLower(m[1]), Owner: m[2], Repo: m[3], Number: n}, true
}

// maxCachedResponses bounds the conditional-request cache; past it the cache
// starts over (the next poll pays full requests once).
const maxCachedResponses = 2000

// maxPages bounds how many pages one listing follows.
const maxPages = 5

// Client reads pull requests, check runs, commit statuses and reviews from
// the GitHub REST API. Every GET is conditional (If-None-Match with the
// response's ETag): GitHub does not count a 304 against the rate limit, so
// polling an unchanged PR is free.
type Client struct {
	// HTTP is the transport; nil means a client with a 30s timeout.
	HTTP *http.Client
	// Token returns the token for a host ("" sends no Authorization).
	Token func(host string) string
	// BaseURL returns the API root for a host; nil means api.github.com for
	// github.com and https://<host>/api/v3 for anything else.
	BaseURL func(host string) string

	mu    sync.Mutex
	cache map[string]cachedResponse
	rate  *tracker.RateLimitSnapshot
}

type cachedResponse struct {
	etag string
	body []byte
	link string
}

// ErrNotFound is returned for a 404 (a deleted PR, or a token without access
// to the repository).
var ErrNotFound = errors.New("prwatch: not found")

// PullRequest is the part of a pull request the watcher reads.
type PullRequest struct {
	State          string `json:"state"` // open | closed
	Merged         bool   `json:"merged"`
	MergeableState string `json:"mergeable_state"`
	HTMLURL        string `json:"html_url"`
	Number         int    `json:"number"`
	Head           struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	// Base and Labels are read by change review (#238).
	Base struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// CheckRun is one check run.
type CheckRun struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	CompletedAt time.Time `json:"completed_at"`
}

// Status is one commit status.
type Status struct {
	ID        int64     `json:"id"`
	Context   string    `json:"context"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Review is one pull request review.
type Review struct {
	ID          int64     `json:"id"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	User        struct {
		Login string `json:"login"`
	} `json:"user"`
}

// PR reads the pull request.
func (c *Client) PR(ctx context.Context, ref PRRef) (PullRequest, error) {
	var pr PullRequest
	_, err := c.get(ctx, ref.Host, fmt.Sprintf("/repos/%s/%s/pulls/%d", ref.Owner, ref.Repo, ref.Number), &pr)
	return pr, err
}

// CheckRuns lists the check runs on sha.
func (c *Client) CheckRuns(ctx context.Context, ref PRRef, sha string) ([]CheckRun, error) {
	var all []CheckRun
	next := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", ref.Owner, ref.Repo, url.PathEscape(sha))
	for page := 0; next != "" && page < maxPages; page++ {
		var body struct {
			CheckRuns []CheckRun `json:"check_runs"`
		}
		var err error
		if next, err = c.get(ctx, ref.Host, next, &body); err != nil {
			return nil, err
		}
		all = append(all, body.CheckRuns...)
	}
	return all, nil
}

// Statuses lists the commit statuses on sha, newest first.
func (c *Client) Statuses(ctx context.Context, ref PRRef, sha string) ([]Status, error) {
	var all []Status
	next := fmt.Sprintf("/repos/%s/%s/commits/%s/statuses?per_page=100", ref.Owner, ref.Repo, url.PathEscape(sha))
	for page := 0; next != "" && page < maxPages; page++ {
		var body []Status
		var err error
		if next, err = c.get(ctx, ref.Host, next, &body); err != nil {
			return nil, err
		}
		all = append(all, body...)
	}
	return all, nil
}

// Reviews lists the reviews on the pull request, oldest first.
func (c *Client) Reviews(ctx context.Context, ref PRRef) ([]Review, error) {
	var all []Review
	next := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100", ref.Owner, ref.Repo, ref.Number)
	for page := 0; next != "" && page < maxPages; page++ {
		var body []Review
		var err error
		if next, err = c.get(ctx, ref.Host, next, &body); err != nil {
			return nil, err
		}
		all = append(all, body...)
	}
	return all, nil
}

// RateLimitSnapshot implements tracker.RateLimiter with the last
// X-RateLimit-* headers seen (nil before any).
func (c *Client) RateLimitSnapshot() *tracker.RateLimitSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rate == nil {
		return nil
	}
	cp := *c.rate
	return &cp
}

func (c *Client) baseURL(host string) string {
	if c.BaseURL != nil {
		return strings.TrimRight(c.BaseURL(host), "/")
	}
	if host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

// get GETs path (or an absolute next-page URL) into out and returns the next
// page's URL from the Link header ("" when there is none).
func (c *Client) get(ctx context.Context, host, path string, out any) (string, error) {
	target := path
	if !strings.HasPrefix(path, "https://") && !strings.HasPrefix(path, "http://") {
		target = c.baseURL(host) + path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("prwatch: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != nil {
		if tok := c.Token(host); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	c.mu.Lock()
	cached, haveCached := c.cache[target]
	c.mu.Unlock()
	if haveCached {
		req.Header.Set("If-None-Match", cached.etag)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("prwatch: GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	c.noteRateLimit(resp)
	var body []byte
	link := resp.Header.Get("Link")
	switch {
	case resp.StatusCode == http.StatusNotModified && haveCached:
		body, link = cached.body, cached.link
	case resp.StatusCode == http.StatusNotFound:
		return "", ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("prwatch: GET %s: HTTP %d", path, resp.StatusCode)
	default:
		body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return "", fmt.Errorf("prwatch: GET %s: %w", path, err)
		}
		if etag := resp.Header.Get("ETag"); etag != "" {
			c.mu.Lock()
			if c.cache == nil || len(c.cache) >= maxCachedResponses {
				c.cache = make(map[string]cachedResponse)
			}
			c.cache[target] = cachedResponse{etag: etag, body: body, link: link}
			c.mu.Unlock()
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return "", fmt.Errorf("prwatch: GET %s: decode: %w", path, err)
	}
	// Only follow a next page on the same API root: the token goes with it.
	if next := nextLink(link); strings.HasPrefix(next, c.baseURL(host)+"/") {
		return next, nil
	}
	return "", nil
}

func (c *Client) noteRateLimit(resp *http.Response) {
	limit, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Limit"))
	if err != nil || limit <= 0 {
		return
	}
	remaining, _ := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	snap := &tracker.RateLimitSnapshot{RequestsLimit: limit, RequestsRemaining: remaining}
	if ts, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		t := time.Unix(ts, 0)
		snap.Reset = &t
	}
	c.mu.Lock()
	c.rate = snap
	c.mu.Unlock()
}

var nextLinkRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func nextLink(link string) string {
	if m := nextLinkRe.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
}
