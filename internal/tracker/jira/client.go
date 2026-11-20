// Package jira implements the tracker adapter for Jira Cloud (REST API v3).
package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/tracker"
)

const (
	adapterName = "jira"
	httpTimeout = 30 * time.Second
	// maxPages caps search and comment pagination so a server that never
	// stops paging cannot loop forever.
	maxPages = 1000
	// defaultCommentPageSize is the comment page size Jira allows by default.
	defaultCommentPageSize = 100
)

// errBadEndpoint is what every request returns when the endpoint is not safe
// to send Basic credentials to. It never prints the endpoint, which may
// carry a secret.
var errBadEndpoint = errors.New("jira: tracker.endpoint must be an https URL without credentials in it (http is allowed for a loopback host only)")

var (
	issueIDPattern  = regexp.MustCompile(`^[0-9]+$`)
	issueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[0-9]+$`)
)

// ClientConfig holds configuration for the Jira Cloud REST tracker adapter.
type ClientConfig struct {
	// Endpoint is the site base URL, e.g. https://your-site.atlassian.net.
	Endpoint string
	// APIKey is "email:api_token"; it is sent as HTTP Basic auth.
	APIKey string
	// ProjectSlug is the Jira project key, e.g. "PROJ".
	ProjectSlug  string
	ActiveStates []string
	// Assignee narrows every list read to the issues assigned to one
	// account: "me" (any case: the token's account) or an Atlassian
	// account id. Empty: anyone. Reads by id are never narrowed.
	Assignee string
	// Labels narrows every list read to the issues carrying any of these
	// labels, ANDed with the project and the assignee. Empty: no label
	// filter.
	Labels []string
}

// Client is the Jira Cloud REST tracker adapter.
type Client struct {
	cfg ClientConfig
	// statesMu guards cfg.ActiveStates, which SetStateLists replaces at
	// runtime. Read it through activeStates.
	statesMu   sync.RWMutex
	httpClient *http.Client
	// endpointErr is set when the endpoint is not acceptable; do returns it
	// before it builds any request.
	endpointErr error
	maxPages    int
	// commentPageSize is the maxResults asked of each comment page.
	commentPageSize int
	// meMu guards me, the account id of the token, read once for an
	// assignee of "me" (see resolveMe).
	meMu sync.Mutex
	me   string
}

var (
	_ tracker.IdempotentCommenter = (*Client)(nil)
	_ tracker.RateLimiter         = (*Client)(nil)
	_ tracker.ScopeMatcher        = (*Client)(nil)
	_ tracker.StateListSetter     = (*Client)(nil)
	_ tracker.Tracker             = (*Client)(nil)
)

// RateLimitSnapshot implements tracker.RateLimiter and always returns nil.
// Jira gives API-token traffic no hourly budget: it is held to per-second
// burst limits, and the X-RateLimit-Limit and X-RateLimit-Remaining headers
// count requests per second for one endpoint
// (https://developer.atlassian.com/cloud/jira/platform/rate-limiting/). A
// snapshot filled from them would show the dashboard a false budget, and
// one busy second under the write reserve would shed a whole tick's polling
// reads. Every caller reads nil as "unknown" and fails open: no shedding, no
// slower automation polls, no budget on the dashboard. A 429 is still
// retried after its Retry-After (tracker.JiraRateLimitClassifier). The
// tracker store needs the method to wrap the adapter (#113).
func (c *Client) RateLimitSnapshot() *tracker.RateLimitSnapshot { return nil }

// NewClient creates a Jira Client. A single trailing "/" on cfg.Endpoint is
// trimmed so request paths join cleanly. The assignee is trimmed, and the
// labels are trimmed and de-duplicated without case, as JQL compares them.
func NewClient(cfg ClientConfig) *Client {
	cfg.Endpoint = strings.TrimSuffix(cfg.Endpoint, "/")
	cfg.Assignee = strings.TrimSpace(cfg.Assignee)
	cfg.Labels = cleanLabels(cfg.Labels)
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: httpTimeout,
			// The adapter never follows a redirect: Basic credentials must
			// not follow one to another scheme or host. A 3xx becomes a
			// tracker.APIStatusError.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		endpointErr:     checkEndpoint(cfg.Endpoint),
		maxPages:        maxPages,
		commentPageSize: defaultCommentPageSize,
	}
}

// checkEndpoint accepts an https URL without userinfo, and an http URL only
// for a loopback host. Credentials sent to a loopback host do not leave the
// machine, and local fakes and tests use httptest servers on 127.0.0.1.
func checkEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Host == "" {
		return errBadEndpoint
	}
	host := u.Hostname()
	loopback := host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	if u.Scheme == "https" || (u.Scheme == "http" && loopback) {
		return nil
	}
	return errBadEndpoint
}

// SetStateLists replaces the active state list at runtime, so a dashboard
// save takes effect on the next fetch without a reload. The adapter keeps no
// terminal list: Jira statuses are plain names, and no read depends on which
// of them are terminal. It is safe for concurrent use with every other
// method.
func (c *Client) SetStateLists(active, _ []string) {
	c.statesMu.Lock()
	defer c.statesMu.Unlock()
	c.cfg.ActiveStates = append([]string(nil), active...)
}

// activeStates returns a copy of the current active state names.
func (c *Client) activeStates() []string {
	c.statesMu.RLock()
	defer c.statesMu.RUnlock()
	return append([]string(nil), c.cfg.ActiveStates...)
}

// do sends one authenticated request to the Jira site and decodes a 2xx JSON
// response into out; a nil out skips decoding, so a 204 or an empty 2xx
// body succeeds. A nil body sends no payload. 404 becomes a
// tracker.NotFoundError and any other non-2xx, a redirect included, a
// tracker.APIStatusError. It returns the endpoint error before any request
// when the endpoint is not acceptable. The
// API key and Authorization header are never logged.
func (c *Client) do(ctx context.Context, method, pathAndQuery string, body, out any) error {
	if c.endpointErr != nil {
		return c.endpointErr
	}
	var rdr io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.Endpoint+pathAndQuery, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.cfg.APIKey)))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := tracker.DoWithRateLimitRetry(ctx, c.httpClient, req, adapterName, tracker.JiraRateLimitClassifier)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return &tracker.NotFoundError{Adapter: adapterName}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return &tracker.APIStatusError{Adapter: adapterName, Status: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}
