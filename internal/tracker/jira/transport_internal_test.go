package jira

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPollingPostsCarryReadIntent(t *testing.T) {
	var mu sync.Mutex
	readIntent := map[string]bool{}
	c := NewClient(ClientConfig{Endpoint: "https://site.example", APIKey: "me@example.com:tok", ProjectSlug: "PROJ", ActiveStates: []string{"To Do"}})
	c.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		readIntent[r.Method+" "+r.URL.Path] = tracker.HasReadIntent(r.Context())
		mu.Unlock()
		body := `{"issues":[],"isLast":true}`
		switch {
		case r.URL.Path == "/rest/api/3/comment/list":
			body = `{"values":[{"id":"1","properties":[]}]}`
		case strings.HasSuffix(r.URL.Path, "/comment"):
			body = `{"comments":[{"id":"1"}],"total":1}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}

	_, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	_, err = c.FetchIssueStatesByIDs(t.Context(), []string{"10001"})
	require.NoError(t, err)
	_, err = c.FetchIssueDetail(t.Context(), "PROJ-1")
	require.NoError(t, err)

	assert.True(t, readIntent["POST /rest/api/3/search/jql"], "search is a read")
	assert.True(t, readIntent["POST /rest/api/3/issue/bulkfetch"], "bulkfetch is a read")
	assert.True(t, readIntent["POST /rest/api/3/comment/list"], "the comment property read is a read")
}

// refusingClient returns a client for endpoint whose transport counts every
// request that reaches it, so a test can show that none did.
func refusingClient(endpoint string) (*Client, *atomic.Int64) {
	var hits atomic.Int64
	c := NewClient(ClientConfig{Endpoint: endpoint, APIKey: "me@example.com:tok", ProjectSlug: "PROJ", ActiveStates: []string{"To Do"}})
	c.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return nil, io.ErrUnexpectedEOF
	})}
	return c, &hits
}

// everyMethod calls each read and write method once and returns their errors.
func everyMethod(c *Client) map[string]error {
	ctx := context.Background()
	errs := map[string]error{}
	_, errs["FetchCandidateIssues"] = c.FetchCandidateIssues(ctx)
	_, errs["FetchIssueStatesByIDs"] = c.FetchIssueStatesByIDs(ctx, []string{"1"})
	_, errs["FetchIssueDetail"] = c.FetchIssueDetail(ctx, "PROJ-1")
	_, errs["CreateComment"] = c.CreateComment(ctx, "PROJ-1", "hi")
	errs["UpdateIssueState"] = c.UpdateIssueState(ctx, "PROJ-1", "Done")
	errs["SetIssueBranch"] = c.SetIssueBranch(ctx, "PROJ-1", "b")
	_, errs["CreateIssue"] = c.CreateIssue(ctx, "", tracker.NewIssue{Title: "t"})
	_, errs["ProbeProjectAccess"] = c.ProbeProjectAccess(ctx)
	return errs
}

func TestClientRefusesPlainHTTPToNonLoopbackHost(t *testing.T) {
	c, hits := refusingClient("http://jira.example.test")

	for name, err := range everyMethod(c) {
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "tracker.endpoint must be an https URL without credentials in it", name)
	}
	assert.Zero(t, hits.Load(), "the check runs before any request")
}

func TestClientRefusesEndpointWithUserinfo(t *testing.T) {
	c, hits := refusingClient("https://me%40example.com:SECRET-TOKEN@jira.example.test")

	for name, err := range everyMethod(c) {
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "without credentials in it", name)
		assert.NotContains(t, err.Error(), "SECRET-TOKEN", name)
	}
	assert.Zero(t, hits.Load())
}

func TestClientAllowsLoopbackHTTP(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		c, hits := refusingClient("http://" + host + ":8080")
		_, err := c.FetchCandidateIssues(t.Context())
		require.Error(t, err, host) // the stub transport fails, but the request was sent
		assert.NotContains(t, err.Error(), "tracker.endpoint must be", host)
		assert.Equal(t, int64(1), hits.Load(), host)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHits.Add(1) }))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	c := NewClient(ClientConfig{Endpoint: redirector.URL, APIKey: "me@example.com:tok", ProjectSlug: "PROJ"})

	_, err := c.ProbeProjectAccess(t.Context())

	var apiErr *tracker.APIStatusError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusFound, apiErr.Status)
	assert.Zero(t, targetHits.Load(), "the redirect target gets no request")
}
