package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

const defaultEndpoint = "https://api.github.com"
const httpTimeout = 30 * time.Second
const pageSize = 50

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// ErrMissingPageLink is returned when a non-empty Link header contains no rel="next" entry.
var ErrMissingPageLink = errors.New("github_missing_page_link")

// ClientConfig holds configuration for the GitHub REST tracker adapter.
type ClientConfig struct {
	APIKey         string
	ProjectSlug    string // "owner/repo"
	ActiveStates   []string
	TerminalStates []string
	BacklogStates  []string
	Endpoint       string
}

// rateLimitSnapshot holds the most recent X-RateLimit-* values observed.
type rateLimitSnapshot struct {
	limit     int
	remaining int
	reset     *time.Time
}

// blockerStateCacheTTL bounds how long a successfully fetched blocker state
// is reused before populateBlockerStates re-fetches it from GitHub.
//
// Set well within the dependency-audit refresh interval's freshness
// expectations (config.DefaultDependencyAuditRefreshIntervalMs, 10 minutes
// by default) so a cached read never outlives the audit's own staleness
// budget; it also bounds the worst-case delay before an unblock (a closed
// blocker) is observed by a dependent issue. Not configurable — YAGNI until
// an operator actually needs a different value; this is a single
// bandwidth-vs-freshness tradeoff with one obviously correct default.
//
// Staleness compounds with the dependency-audit refresh path rather than
// replacing it: a watched-but-inactive issue is only re-evaluated on that
// path's own interval (10 minutes by default), so the worst case for
// observing blockers_resolved through that path is roughly TTL + refresh
// interval — about 15 minutes, up from ~10 minutes pre-cache. The fail
// direction is safe: a stale cache entry can only read as "still blocked"
// (state unchanged since the last successful fetch), never as a false
// unblock, so the extra delay costs latency, not correctness.
const blockerStateCacheTTL = 5 * time.Minute

// blockerCacheEntry is a cached populateBlockerStates result for one blocker
// issue. Only successful fetches are stored — see populateBlockerStates.
type blockerCacheEntry struct {
	state     string
	url       string
	fetchedAt time.Time
}

// Client is the GitHub Issues REST tracker adapter.
type Client struct {
	cfg           ClientConfig
	httpClient    *http.Client
	owner         string
	repo          string
	rateMu        sync.RWMutex
	lastRateLimit *rateLimitSnapshot

	// blockerCacheMu guards blockerCache, the TTL cache of blocker states
	// used by populateBlockerStates (see blockerStateCacheTTL). It is keyed
	// by blocker issue ID and lives for the process lifetime, capped by TTL
	// per entry rather than by eviction.
	//
	// A FAILED fetch is never stored here: populateBlockerStates only calls
	// storeBlockerState for a SUCCESSFUL GET (its result's ok == true), so a
	// transient GitHub error (network blip, rate limit, 5xx) re-fetches on
	// the very next poll instead of suppressing state resolution for the
	// whole TTL window. Success is judged by whether the GET returned an
	// issue, not by whether that issue resolved to a non-empty state — an
	// open, unlabeled blocker (deriveState returns "") is a valid, cacheable
	// answer, and the common case for "depends on #N" references to
	// untriaged prerequisites; treating it as "no cache entry" would defeat
	// the cache for exactly that population.
	blockerCacheMu sync.RWMutex
	blockerCache   map[string]blockerCacheEntry

	// now returns the current time. Defaults to time.Now; overridable in
	// tests (see export_test.go) to exercise blockerStateCacheTTL expiry
	// without sleeping.
	now func() time.Time
}

// NewClient creates a new GitHub Client.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	owner, repo, _ := strings.Cut(cfg.ProjectSlug, "/")
	return &Client{
		cfg:          cfg,
		httpClient:   &http.Client{Timeout: httpTimeout},
		owner:        owner,
		repo:         repo,
		blockerCache: make(map[string]blockerCacheEntry),
		now:          time.Now,
	}
}

// lookupBlockerState returns the cached state for blocker id if it was
// fetched successfully within blockerStateCacheTTL. ok is false on a cache
// miss or an expired entry.
func (c *Client) lookupBlockerState(id string) (blockerCacheEntry, bool) {
	c.blockerCacheMu.RLock()
	defer c.blockerCacheMu.RUnlock()
	entry, ok := c.blockerCache[id]
	if !ok || c.now().Sub(entry.fetchedAt) >= blockerStateCacheTTL {
		return blockerCacheEntry{}, false
	}
	return entry, true
}

// storeBlockerState caches a successfully fetched blocker state. Callers
// must not call this for a failed fetch — see blockerCacheMu's doc comment.
func (c *Client) storeBlockerState(id, state, url string) {
	c.blockerCacheMu.Lock()
	defer c.blockerCacheMu.Unlock()
	c.blockerCache[id] = blockerCacheEntry{state: state, url: url, fetchedAt: c.now()}
}

// FetchCandidateIssues fetches open issues filtered by active-state labels, paginated.
// GitHub's label filter is AND-semantics (issues must have ALL listed labels), so we
// issue one request per active state and deduplicate by issue ID.
func (c *Client) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	seen := make(map[string]struct{})
	var all []domain.Issue
	for _, activeState := range c.cfg.ActiveStates {
		q := url.Values{}
		q.Set("state", "open")
		q.Set("labels", activeState)
		q.Set("per_page", strconv.Itoa(pageSize))
		u := fmt.Sprintf("%s/repos/%s/%s/issues?%s", c.cfg.Endpoint, c.owner, c.repo, q.Encode())
		issues, err := c.fetchPaginated(ctx, u, c.cfg.ActiveStates)
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			if _, dup := seen[issue.ID]; !dup {
				seen[issue.ID] = struct{}{}
				all = append(all, issue)
			}
		}
	}
	all = c.populateBlockerStates(ctx, all)
	return all, nil
}

// FetchIssuesByStates fetches issues by state category.
// For "closed" state: fetches closed issues.
// For label-based states: fetches open issues with those labels.
func (c *Client) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return []domain.Issue{}, nil
	}

	var closedStates, labelStates []string
	for _, s := range stateNames {
		if strings.ToLower(s) == "closed" {
			closedStates = append(closedStates, s)
		} else {
			labelStates = append(labelStates, s)
		}
	}

	var all []domain.Issue

	if len(closedStates) > 0 {
		q := url.Values{}
		q.Set("state", "closed")
		q.Set("per_page", strconv.Itoa(pageSize))
		u := fmt.Sprintf("%s/repos/%s/%s/issues?%s", c.cfg.Endpoint, c.owner, c.repo, q.Encode())
		issues, err := c.fetchPaginated(ctx, u, closedStates)
		if err != nil {
			return nil, err
		}
		all = append(all, issues...)
	}

	if len(labelStates) > 0 {
		// GitHub label filter is AND-semantics; fetch one label at a time and deduplicate.
		seen := make(map[string]struct{})
		for _, labelState := range labelStates {
			q := url.Values{}
			q.Set("state", "open")
			q.Set("labels", labelState)
			q.Set("per_page", strconv.Itoa(pageSize))
			u := fmt.Sprintf("%s/repos/%s/%s/issues?%s", c.cfg.Endpoint, c.owner, c.repo, q.Encode())
			issues, err := c.fetchPaginated(ctx, u, labelStates)
			if err != nil {
				return nil, err
			}
			for _, issue := range issues {
				if _, dup := seen[issue.ID]; !dup {
					seen[issue.ID] = struct{}{}
					all = append(all, issue)
				}
			}
		}
	}

	return c.populateBlockerStates(ctx, all), nil
}

// maxConcurrentFetches caps concurrent goroutines in boundedDo.
const maxConcurrentFetches = 8

// boundedDo runs fn for each item in items with at most maxConcurrentFetches
// goroutines in flight simultaneously. fn receives the item index and value.
// The caller is responsible for goroutine-safe result collection (e.g. a
// pre-allocated slice or a buffered channel closed after boundedDo returns).
func boundedDo[T any](ctx context.Context, items []T, fn func(ctx context.Context, idx int, item T)) {
	sem := make(chan struct{}, maxConcurrentFetches)
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, it T) {
			defer func() { <-sem }()
			defer wg.Done()
			fn(ctx, i, it)
		}(i, item)
	}
	wg.Wait()
}

// FetchIssueStatesByIDs fetches each issue individually (GitHub has no batch endpoint).
// If any single request fails, the entire operation returns an error.
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return []domain.Issue{}, nil
	}

	type fetchResult struct {
		issue *domain.Issue
		err   error
		idx   int
	}

	ch := make(chan fetchResult, len(issueIDs))
	boundedDo(ctx, issueIDs, func(ctx context.Context, idx int, issueID string) {
		issue, err := c.fetchSingleIssue(ctx, issueID)
		ch <- fetchResult{issue: issue, err: err, idx: idx}
	})
	close(ch)

	issues := make([]domain.Issue, len(issueIDs))
	for r := range ch {
		if r.err != nil {
			if errors.Is(r.err, tracker.ErrNotFound) {
				continue // deleted or transferred — reconciler will stop the worker
			}
			return nil, r.err
		}
		if r.issue != nil {
			issues[r.idx] = *r.issue
		}
	}

	// Filter nil slots (missing issues)
	var out []domain.Issue
	for _, issue := range issues {
		if issue.ID != "" {
			out = append(out, issue)
		}
	}
	return out, nil
}

// FetchIssueDetail returns a single issue with its full comment thread.
// issueID is the numeric issue number as a string (e.g. "42").
func (c *Client) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	// Fetch issue body.
	issueURL := fmt.Sprintf("%s/repos/%s/%s/issues/%s", c.cfg.Endpoint, c.owner, c.repo, issueID)
	issueBody, _, err := c.get(ctx, issueURL)
	if err != nil {
		return nil, fmt.Errorf("github_fetch_issue_detail: %w", err)
	}
	raw, ok := issueBody.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("github_fetch_issue_detail: unexpected issue shape")
	}
	derived := deriveState(raw, c.cfg.ActiveStates, c.cfg.TerminalStates)
	issue := normalizeIssue(raw, derived)
	if issue == nil {
		return nil, fmt.Errorf("issue %s not found or missing required fields", issueID)
	}

	// Fetch comments and attach them to the issue.
	commentsURL := fmt.Sprintf("%s/repos/%s/%s/issues/%s/comments?per_page=%d",
		c.cfg.Endpoint, c.owner, c.repo, issueID, pageSize)
	for commentsURL != "" {
		commentsBody, linkHeader, err := c.get(ctx, commentsURL)
		if err != nil {
			// Non-fatal: return the issue without comments rather than failing entirely.
			break
		}
		rawComments, ok := commentsBody.([]any)
		if !ok {
			break
		}
		for _, item := range rawComments {
			cm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			body, _ := cm["body"].(string)
			if body == "" {
				continue
			}
			// Extract branch name from hidden itervox marker; skip adding to Comments.
			if branch, ok := strings.CutPrefix(body, itervoxBranchPrefix); ok {
				branch = strings.TrimSuffix(strings.TrimSpace(branch), "-->")
				branch = strings.TrimSpace(branch)
				if branch != "" {
					b := branch
					issue.BranchName = &b // last marker wins (most recent)
				}
				continue
			}
			var authorName string
			var authorID string
			if user, ok := cm["user"].(map[string]any); ok {
				authorName, _ = user["login"].(string)
				if id, ok := tracker.ToIntVal(user["id"]); ok {
					authorID = strconv.Itoa(id)
				}
			}
			commentID := ""
			if id, ok := tracker.ToIntVal(cm["id"]); ok {
				commentID = strconv.Itoa(id)
			}
			comment := domain.Comment{
				ID:         commentID,
				Body:       body,
				CreatedAt:  tracker.ParseTime(cm["created_at"]),
				AuthorID:   authorID,
				AuthorName: authorName,
			}
			issue.Comments = append(issue.Comments, comment)
		}
		next, err := ParseNextLink(linkHeader)
		if err != nil || next == "" {
			break
		}
		commentsURL = next
	}

	// TRK-2: this is a dependency-audit refresh path — blocker states must be
	// populated here too, not just in FetchCandidateIssues, or
	// blockers_resolved can never fire for non-active watched issues.
	// populateBlockerStates no-ops cheaply when issue.BlockedBy has no IDs.
	populated := c.populateBlockerStates(ctx, []domain.Issue{*issue})
	return &populated[0], nil //nolint:nilerr // comment-fetch errors are non-fatal; we break and return the issue without comments
}

// fetchSingleIssue fetches one GitHub issue by its number (as string ID).
func (c *Client) fetchSingleIssue(ctx context.Context, issueNumber string) (*domain.Issue, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/issues/%s", c.cfg.Endpoint, c.owner, c.repo, issueNumber)
	body, _, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}

	raw, ok := body.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("github_unknown_payload: unexpected issue shape")
	}

	derived := deriveState(raw, c.cfg.ActiveStates, c.cfg.TerminalStates)
	issue := normalizeIssue(raw, derived)
	return issue, nil
}

// fetchPaginated follows Link header pagination for a GitHub list endpoint.
// extraStates lists additional label names (e.g. backlog_states) that are
// accepted even when absent from active_states and terminal_states.
func (c *Client) fetchPaginated(ctx context.Context, startURL string, extraStates []string) ([]domain.Issue, error) {
	var all []domain.Issue
	nextURL := startURL

	for nextURL != "" {
		body, linkHeader, err := c.get(ctx, nextURL)
		if err != nil {
			return nil, err
		}

		rawItems, ok := body.([]any)
		if !ok {
			return nil, fmt.Errorf("github_unknown_payload: expected array response")
		}

		for _, item := range rawItems {
			raw, ok := item.(map[string]any)
			if !ok {
				continue
			}
			derived := deriveState(raw, c.cfg.ActiveStates, c.cfg.TerminalStates)
			if derived == "" {
				// Fall back to extraStates (e.g. backlog_states) so issues whose
				// labels are not in active/terminal are still returned.
				for _, label := range extractLabels(raw) {
					for _, extra := range extraStates {
						if strings.EqualFold(label, extra) {
							derived = extra
							break
						}
					}
					if derived != "" {
						break
					}
				}
			}
			if derived == "" {
				continue // not eligible
			}
			if issue := normalizeIssue(raw, derived); issue != nil {
				all = append(all, *issue)
			}
		}

		next, err := ParseNextLink(linkHeader)
		if err != nil {
			// ErrMissingPageLink means the last page had no rel="next" — treat as done.
			break
		}
		nextURL = next
	}

	return all, nil //nolint:nilerr // link-parse errors mean end of pagination, not a real error
}

// UpdateIssueState manages labels to simulate workflow state transitions.
// It removes any existing active/terminal state labels and adds the target label.
func (c *Client) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	u := fmt.Sprintf("%s/repos/%s/%s/issues/%s/labels", c.cfg.Endpoint, c.owner, c.repo, issueID)

	// Remove existing state labels (active + terminal + backlog).
	// Use a fresh slice to avoid mutating cfg's backing arrays.
	allStateLabels := make([]string, 0, len(c.cfg.ActiveStates)+len(c.cfg.TerminalStates)+len(c.cfg.BacklogStates))
	allStateLabels = append(allStateLabels, c.cfg.ActiveStates...)
	allStateLabels = append(allStateLabels, c.cfg.TerminalStates...)
	allStateLabels = append(allStateLabels, c.cfg.BacklogStates...)
	for _, label := range allStateLabels {
		if strings.EqualFold(label, stateName) {
			continue
		}
		delU := fmt.Sprintf("%s/repos/%s/%s/issues/%s/labels/%s",
			c.cfg.Endpoint, c.owner, c.repo, issueID, url.PathEscape(label))
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, delU, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		resp, err := tracker.DoWithRateLimitRetry(ctx, c.httpClient, req, "github")
		if err != nil {
			slog.Warn("github_update_state: remove label request failed (ignored)",
				"label", label, "issue_id", issueID, "error", err)
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			slog.Warn("github_update_state: unexpected status removing label (ignored)",
				"label", label, "issue_id", issueID, "status", resp.StatusCode)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	// Add the target state label.
	payload, err := json.Marshal(map[string][]string{"labels": {stateName}})
	if err != nil {
		return fmt.Errorf("github_update_state: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github_update_state: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := tracker.DoWithRateLimitRetry(ctx, c.httpClient, req, "github")
	if err != nil {
		return fmt.Errorf("github_update_state: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github_update_state: status %d", resp.StatusCode)
	}
	return nil
}

// itervoxBranchPrefix is used to embed the branch name in a hidden HTML
// comment so it survives round-trips without polluting the issue UI.
const itervoxBranchPrefix = "<!-- itervox:branch:"

// SetIssueBranch posts a hidden HTML comment recording the branch name on the
// GitHub issue. FetchIssueDetail scans for this comment to restore BranchName
// on subsequent fetches, enabling retried workers to resume the correct branch.
func (c *Client) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	body := itervoxBranchPrefix + branchName + " -->"
	_, err := c.CreateComment(ctx, issueID, body)
	return err
}

// FetchIssueByIdentifier returns a single issue by its human-readable identifier
// (e.g. "#42"). The leading "#" is stripped before calling FetchIssueDetail.
func (c *Client) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	issueID := strings.TrimPrefix(identifier, "#")
	return c.FetchIssueDetail(ctx, issueID)
}

// CreateComment posts a comment on the GitHub issue identified by issueID.
// issueID is expected to be the numeric issue number (as a string, e.g. "42").
func (c *Client) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/issues/%s/comments", c.cfg.Endpoint, c.owner, c.repo, issueID)
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return nil, fmt.Errorf("github_create_comment: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("github_create_comment: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := tracker.DoWithRateLimitRetry(ctx, c.httpClient, req, "github")
	if err != nil {
		return nil, fmt.Errorf("github_create_comment: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("github_create_comment: status %d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("github_create_comment: decode body: %w", err)
	}
	comment := &domain.Comment{Body: body}
	if id, ok := tracker.ToIntVal(raw["id"]); ok {
		comment.ID = strconv.Itoa(id)
	}
	comment.CreatedAt = tracker.ParseTime(raw["created_at"])
	if user, ok := raw["user"].(map[string]any); ok {
		if id, ok := tracker.ToIntVal(user["id"]); ok {
			comment.AuthorID = strconv.Itoa(id)
		}
		comment.AuthorName, _ = user["login"].(string)
	}
	if postedBody, ok := raw["body"].(string); ok && postedBody != "" {
		comment.Body = postedBody
	}
	return comment, nil
}

// CreateIssue creates a new GitHub issue in the configured repository. The
// sourceIssueID is accepted for tracker interface parity but not otherwise used.
func (c *Client) CreateIssue(ctx context.Context, _ string, title, body, stateName string) (*domain.Issue, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/issues", c.cfg.Endpoint, c.owner, c.repo)
	payloadBody := map[string]any{
		"title": title,
		"body":  body,
	}
	if stateName = strings.TrimSpace(stateName); stateName != "" {
		payloadBody["labels"] = []string{stateName}
	}
	payload, err := json.Marshal(payloadBody)
	if err != nil {
		return nil, fmt.Errorf("github_create_issue: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("github_create_issue: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := tracker.DoWithRateLimitRetry(ctx, c.httpClient, req, "github")
	if err != nil {
		return nil, fmt.Errorf("github_create_issue: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("github_create_issue: status %d", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("github_create_issue: decode body: %w", err)
	}
	derived := deriveState(raw, c.cfg.ActiveStates, c.cfg.TerminalStates)
	if derived == "" {
		derived = stateName
	}
	issue := normalizeIssue(raw, derived)
	if issue == nil {
		return nil, fmt.Errorf("github_create_issue: missing issue fields in response")
	}
	return issue, nil
}

func (c *Client) get(ctx context.Context, url string) (any, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("github_api_request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := tracker.DoWithRateLimitRetry(ctx, c.httpClient, req, "github")
	if err != nil {
		return nil, "", fmt.Errorf("github_api_request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	c.snapshotRateLimit(resp)

	if resp.StatusCode == http.StatusNotFound {
		return nil, "", &tracker.NotFoundError{Adapter: "github"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", &tracker.APIStatusError{Adapter: "github", Status: resp.StatusCode}
	}

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("github_api_request: read body: %w", err)
	}

	var result any
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return nil, "", fmt.Errorf("github_api_request: decode json: %w", err)
	}

	linkHeader := resp.Header.Get("Link")
	return result, linkHeader, nil
}

// snapshotRateLimit captures X-RateLimit-* headers from any response.
func (c *Client) snapshotRateLimit(resp *http.Response) {
	limitStr := resp.Header.Get("X-RateLimit-Limit")
	if limitStr == "" {
		return
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		return
	}
	remaining, _ := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	var reset *time.Time
	if ts, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		t := time.Unix(ts, 0)
		reset = &t
	}
	c.rateMu.Lock()
	c.lastRateLimit = &rateLimitSnapshot{limit: limit, remaining: remaining, reset: reset}
	c.rateMu.Unlock()
}

// RateLimits returns the last observed API rate limit snapshot, or zeros if unknown.
func (c *Client) RateLimits() (limit, remaining int, reset *time.Time) {
	c.rateMu.RLock()
	defer c.rateMu.RUnlock()
	if c.lastRateLimit == nil {
		return 0, 0, nil
	}
	return c.lastRateLimit.limit, c.lastRateLimit.remaining, c.lastRateLimit.reset
}

// RateLimitSnapshot implements tracker.RateLimiter so callers can type-assert
// Tracker to tracker.RateLimiter without importing this concrete package.
func (c *Client) RateLimitSnapshot() *tracker.RateLimitSnapshot {
	limit, remaining, reset := c.RateLimits()
	if limit == 0 && remaining == 0 {
		return nil
	}
	return &tracker.RateLimitSnapshot{
		RequestsLimit:     limit,
		RequestsRemaining: remaining,
		Reset:             reset,
	}
}

// populateBlockerStates fetches the current state for each blocker referenced in issues
// and backfills BlockerRef.State. Error handling is fail-safe (spec D4: unknown or
// ambiguous prerequisite state MUST be treated as unmet): ANY fetch error — including
// 404, which GitHub also returns for permission loss and transferred issues, not just
// deletion — leaves State nil so the orchestrator classifies the dependency as unknown
// and keeps dependents blocked. A genuinely deleted blocker surfaces as a permanent
// "unknown" row in the Deps dashboard; the operator resolves it by removing the
// dangling reference from the issue body.
func (c *Client) populateBlockerStates(ctx context.Context, issues []domain.Issue) []domain.Issue {
	seen := make(map[string]struct{})
	var ids []string
	for _, issue := range issues {
		for _, b := range issue.BlockedBy {
			if b.ID != nil {
				if _, ok := seen[*b.ID]; !ok {
					seen[*b.ID] = struct{}{}
					ids = append(ids, *b.ID)
				}
			}
		}
	}
	if len(ids) == 0 {
		return issues
	}

	type result struct {
		id    string
		state string
		url   string
		// ok is true iff the GET succeeded, independent of state. A fetch
		// can succeed and still yield state == "" — deriveState returns ""
		// for an open issue with no active/terminal label, which is the
		// COMMON case for "depends on #N" references to untriaged
		// prerequisites. ok, not state, is what must gate the cache write:
		// keying on state alone would conflate "successfully learned this
		// blocker has no resolvable state" with "the fetch failed", and
		// re-fetch that (very common) population on every single poll —
		// exactly the amplification this cache exists to remove. See
		// storeBlockerState.
		ok bool
	}

	// Serve whatever is already fresh in the cache; only the remainder needs
	// a live GET. This is what keeps the widened phrase matcher's larger ID
	// set from re-hitting GitHub every poll — see blockerStateCacheTTL.
	resultMap := make(map[string]result, len(ids))
	var toFetch []string
	for _, id := range ids {
		if entry, ok := c.lookupBlockerState(id); ok {
			resultMap[id] = result{id: id, state: entry.state, url: entry.url, ok: true}
			continue
		}
		toFetch = append(toFetch, id)
	}

	if len(toFetch) > 0 {
		ch := make(chan result, len(toFetch))
		boundedDo(ctx, toFetch, func(ctx context.Context, _ int, id string) {
			issue, err := c.fetchSingleIssue(ctx, id)
			if err != nil {
				// D4 fail-safe: ALL fetch errors — including 404, which GitHub returns
				// for permission loss and transferred issues, not just deletion — leave
				// State nil so the orchestrator treats the dependency as unmet. A
				// genuinely deleted blocker surfaces as a permanent "unknown" row in the
				// Deps dashboard; the operator resolves it by removing the reference.
				slog.Error("github: blocker state fetch failed — dependents stay blocked until resolved",
					"blocker_id", id, "error", err)
				ch <- result{id: id}
				return
			}
			if issue == nil {
				ch <- result{id: id}
				return
			}
			url := ""
			if issue.URL != nil {
				url = *issue.URL
			}
			ch <- result{id: id, state: issue.State, url: url, ok: true}
		})
		close(ch)

		for r := range ch {
			resultMap[r.id] = r
			// Cache every SUCCESSFUL fetch, including one that resolved to
			// an empty state (open, unlabeled blocker) — that empty state
			// is itself the correct, stable answer until the blocker is
			// labeled or closed, and re-deriving it every poll is exactly
			// the amplification being fixed here. Only a FAILED fetch
			// (r.ok == false) is left uncached, so a transient GitHub error
			// re-resolves on the very next poll instead of being pinned
			// for the full TTL — see blockerCacheMu.
			if r.ok {
				c.storeBlockerState(r.id, r.state, r.url)
			}
		}
	}

	for i := range issues {
		for j := range issues[i].BlockedBy {
			if issues[i].BlockedBy[j].ID != nil {
				if r, ok := resultMap[*issues[i].BlockedBy[j].ID]; ok {
					// Empty state means the fetch failed transiently — leave
					// State nil so the orchestrator's unknown→blocked
					// fail-safe applies (D4).
					if r.state != "" {
						state := r.state
						issues[i].BlockedBy[j].State = &state
					}
					if r.url != "" {
						url := r.url
						issues[i].BlockedBy[j].URL = &url
					}
				}
			}
		}
	}
	return issues
}

// ParseNextLink extracts the "next" URL from a GitHub Link header.
// Returns ("", nil) when header is empty (no more pages).
// Returns ("", ErrMissingPageLink) when header is non-empty but has no rel="next".
func ParseNextLink(linkHeader string) (string, error) {
	if linkHeader == "" {
		return "", nil
	}
	m := linkNextRe.FindStringSubmatch(linkHeader)
	if m == nil {
		return "", ErrMissingPageLink
	}
	return m[1], nil
}
