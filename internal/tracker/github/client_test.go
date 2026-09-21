package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"errors"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
	ghclient "github.com/vnovick/itervox/internal/tracker/github"
)

func ghIssue(number int, title, state string, labels []string) map[string]interface{} {
	labelObjs := make([]interface{}, len(labels))
	for i, l := range labels {
		labelObjs[i] = map[string]interface{}{"name": l}
	}
	return map[string]interface{}{
		"number":     float64(number),
		"title":      title,
		"state":      state,
		"labels":     labelObjs,
		"html_url":   fmt.Sprintf("https://github.com/owner/repo/issues/%d", number),
		"body":       "",
		"created_at": "2024-01-01T00:00:00Z",
		"updated_at": "2024-01-01T00:00:00Z",
	}
}

type ghServer struct {
	t         *testing.T
	responses []struct {
		body    interface{}
		headers map[string]string
		status  int
	}
	calls int
}

func newGHServer(t *testing.T) *ghServer {
	return &ghServer{t: t}
}

func (s *ghServer) addResponse(body interface{}, linkHeader string, status int) {
	headers := map[string]string{}
	if linkHeader != "" {
		headers["Link"] = linkHeader
	}
	s.responses = append(s.responses, struct {
		body    interface{}
		headers map[string]string
		status  int
	}{body, headers, status})
}

func (s *ghServer) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := s.calls
		s.calls++
		if idx >= len(s.responses) {
			s.t.Errorf("unexpected call %d", idx+1)
			w.WriteHeader(500)
			return
		}
		resp := s.responses[idx]
		for k, v := range resp.headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		if resp.status != 0 {
			w.WriteHeader(resp.status)
		}
		_ = json.NewEncoder(w).Encode(resp.body)
	}))
}

func defaultConfig(endpoint string) ghclient.ClientConfig {
	return ghclient.ClientConfig{
		APIKey:         "ghp_test",
		ProjectSlug:    "owner/repo",
		ActiveStates:   []string{"todo", "in progress"},
		TerminalStates: []string{"closed"},
		Endpoint:       endpoint,
	}
}

func TestGHFetchCandidateIssuesSinglePage(t *testing.T) {
	// One request per active state label ("todo", "in progress").
	// Each returns its own issue; deduplication keeps both.
	srv := newGHServer(t)
	srv.addResponse([]interface{}{ghIssue(1, "Fix bug", "open", []string{"todo"})}, "", 0)
	srv.addResponse([]interface{}{ghIssue(2, "Add feature", "open", []string{"in progress"})}, "", 0)
	ts := srv.serve()
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 2)
	assert.Equal(t, "#1", issues[0].Identifier)
	assert.Equal(t, "#2", issues[1].Identifier)
}

func TestGHFetchCandidateIssuesPaginatedLinkHeader(t *testing.T) {
	mux := http.NewServeMux()
	callCount := 0
	mux.HandleFunc("/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "2" || callCount > 0 {
			callCount++
			_ = json.NewEncoder(w).Encode([]interface{}{ghIssue(2, "Issue 2", "open", []string{"todo"})})
			return
		}
		callCount++
		nextURL := fmt.Sprintf("http://%s/repos/owner/repo/issues?page=2", r.Host)
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
		_ = json.NewEncoder(w).Encode([]interface{}{ghIssue(1, "Issue 1", "open", []string{"todo"})})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	assert.Len(t, issues, 2)
	assert.Equal(t, "#1", issues[0].Identifier)
	assert.Equal(t, "#2", issues[1].Identifier)
}

func TestGHFetchIssuesByStatesEmptyReturnsEmpty(t *testing.T) {
	client := ghclient.NewClient(defaultConfig("http://should-not-be-called.invalid"))
	result, err := client.FetchIssuesByStates(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestGHFetchIssuesByStatesClosedState(t *testing.T) {
	srv := newGHServer(t)
	srv.addResponse([]interface{}{
		ghIssue(10, "Closed issue", "closed", []string{}),
	}, "", 0)
	ts := srv.serve()
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	result, err := client.FetchIssuesByStates(context.Background(), []string{"closed"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "closed", result[0].State)
}

func TestGHFetchIssuesByStatesPaginated(t *testing.T) {
	page1Issues := `[{"number":1,"title":"Issue 1","state":"open","body":"","html_url":"","labels":[{"name":"done"}],"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}]`
	page2Issues := `[{"number":2,"title":"Issue 2","state":"open","body":"","html_url":"","labels":[{"name":"done"}],"created_at":"2024-01-02T00:00:00Z","updated_at":"2024-01-02T00:00:00Z"}]`
	calls := 0
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues?page=2>; rel="next"`, ts.URL))
			_, _ = fmt.Fprint(w, page1Issues)
		} else {
			_, _ = fmt.Fprint(w, page2Issues)
		}
	}))
	defer ts.Close()

	client := ghclient.NewClient(ghclient.ClientConfig{
		APIKey:       "tok",
		ProjectSlug:  "o/r",
		ActiveStates: []string{"done"},
		Endpoint:     ts.URL,
	})
	issues, err := client.FetchIssuesByStates(context.Background(), []string{"done"})
	assert.NoError(t, err)
	assert.Equal(t, 2, len(issues))
}

// TRK-2: the audit refresh paths use FetchIssuesByStates/FetchIssueDetail —
// they must populate blocker states like FetchCandidateIssues does, or
// blockers_resolved can never fire for non-active watched issues.
func TestGHFetchIssuesByStatesPopulatesBlockerStates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("labels") == "blocked" {
			issue := ghIssue(3, "Blocked issue", "open", []string{"blocked"})
			issue["body"] = "Blocked by #10"
			_ = json.NewEncoder(w).Encode([]interface{}{issue})
			return
		}
		_ = json.NewEncoder(w).Encode([]interface{}{})
	})
	mux.HandleFunc("/repos/owner/repo/issues/10", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghIssue(10, "Blocker 10", "closed", []string{}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchIssuesByStates(context.Background(), []string{"blocked"})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Len(t, issues[0].BlockedBy, 1)
	require.NotNil(t, issues[0].BlockedBy[0].State, "refresh path must populate blocker state (TRK-2)")
	assert.Equal(t, "closed", *issues[0].BlockedBy[0].State)
}

// TRK-2: FetchIssueDetail is the other audit refresh path (used to hydrate a
// single watched issue) — it must also populate blocker states.
func TestGHFetchIssueDetailPopulatesBlockerStates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		issue := ghIssue(3, "Blocked issue", "open", []string{"blocked"})
		issue["body"] = "Blocked by #10"
		_ = json.NewEncoder(w).Encode(issue)
	})
	mux.HandleFunc("/repos/owner/repo/issues/3/comments", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]interface{}{})
	})
	mux.HandleFunc("/repos/owner/repo/issues/10", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghIssue(10, "Blocker 10", "closed", []string{}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issue, err := client.FetchIssueDetail(context.Background(), "3")
	require.NoError(t, err)
	require.Len(t, issue.BlockedBy, 1)
	require.NotNil(t, issue.BlockedBy[0].State, "refresh path must populate blocker state (TRK-2)")
	assert.Equal(t, "closed", *issue.BlockedBy[0].State)
}

func TestGHFetchIssueStatesByIDsFanOut(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghIssue(1, "Issue 1", "open", []string{"in progress"}))
	})
	mux.HandleFunc("/repos/owner/repo/issues/2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghIssue(2, "Issue 2", "open", []string{"todo"}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	result, err := client.FetchIssueStatesByIDs(context.Background(), []string{"1", "2"})
	require.NoError(t, err)
	assert.Len(t, result, 2)
	// outbox #54 fast-follow's superseded_by_tracker reconciliation rule
	// (outbox.ReconcileVerdict) depends on UpdatedAt being populated on
	// every issue this method returns — pin it so a future normalize change
	// can't silently drop the field.
	for _, issue := range result {
		require.NotNil(t, issue.UpdatedAt, "issue %s must carry a populated UpdatedAt", issue.Identifier)
		assert.Equal(t, "2024-01-01T00:00:00Z", issue.UpdatedAt.UTC().Format(time.RFC3339))
	}
}

func TestGHFetchIssueStatesByIDs404Skipped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghIssue(1, "Issue 1", "open", []string{"todo"}))
	})
	mux.HandleFunc("/repos/owner/repo/issues/2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	result, err := client.FetchIssueStatesByIDs(context.Background(), []string{"1", "2"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "1", result[0].ID)
}

func TestGHFetchIssueStatesByIDsEmptyReturnsEmpty(t *testing.T) {
	client := ghclient.NewClient(defaultConfig("http://should-not-be-called.invalid"))
	result, err := client.FetchIssueStatesByIDs(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestGHNormalizeClosedIssueAlwaysTerminal(t *testing.T) {
	srv := newGHServer(t)
	srv.addResponse([]interface{}{
		ghIssue(5, "Was open", "closed", []string{"todo"}),
	}, "", 0)
	ts := srv.serve()
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	result, err := client.FetchIssuesByStates(context.Background(), []string{"closed"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "closed", result[0].State)
}

// TestGHClosedIssueNoTerminalLabelFallsBackToFirstTerminalState verifies that
// closing a GitHub issue (state=closed) without applying a terminal label is
// still recognised as terminal by deriveState. Previously this returned ""
// which caused the reconciler to log a misleading "state changed to ”" message
// instead of stopping the worker cleanly.
func TestGHClosedIssueNoTerminalLabelFallsBackToFirstTerminalState(t *testing.T) {
	srv := newGHServer(t)
	// Issue is closed but has no "done"/"cancelled" label — just the default labels.
	srv.addResponse([]interface{}{
		ghIssue(7, "Cancelled work", "closed", []string{"in-progress"}),
	}, "", 0)
	ts := srv.serve()
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.TerminalStates = []string{"done", "cancelled"}
	cfg.ActiveStates = []string{"todo", "in-progress"}
	client := ghclient.NewClient(cfg)
	result, err := client.FetchIssuesByStates(context.Background(), []string{"done"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	// Should return first terminal state, not "" or "closed".
	assert.Equal(t, "done", result[0].State)
}

// TestGHClosedIssueWithTerminalLabelReturnsThatLabel verifies that a closed
// issue with a matching terminal label (e.g. "cancelled") returns that label,
// not the generic fallback.
func TestGHClosedIssueWithTerminalLabelReturnsThatLabel(t *testing.T) {
	srv := newGHServer(t)
	srv.addResponse([]interface{}{
		ghIssue(8, "Cancelled work", "closed", []string{"cancelled"}),
	}, "", 0)
	ts := srv.serve()
	defer ts.Close()

	cfg := defaultConfig(ts.URL)
	cfg.TerminalStates = []string{"done", "cancelled"}
	cfg.ActiveStates = []string{"todo", "in-progress"}
	client := ghclient.NewClient(cfg)
	result, err := client.FetchIssuesByStates(context.Background(), []string{"cancelled"})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "cancelled", result[0].State)
}

func TestGHNormalizeLabelsLowercase(t *testing.T) {
	srv := newGHServer(t)
	srv.addResponse([]interface{}{
		ghIssue(1, "Issue 1", "open", []string{"TODO", "Backend"}),
	}, "", 0)
	srv.addResponse([]interface{}{}, "", 0) // second active-state request ("in progress")
	ts := srv.serve()
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, []string{"todo", "backend"}, issues[0].Labels)
}

func TestGHNormalizeBlockersParsedFromBody(t *testing.T) {
	mux := http.NewServeMux()
	var listCalls atomic.Int32
	mux.HandleFunc("/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if listCalls.Add(1) == 1 {
			issue := ghIssue(3, "Issue with blocker", "open", []string{"todo"})
			issue["body"] = "This is blocked by #10 and also blocked by #20."
			_ = json.NewEncoder(w).Encode([]interface{}{issue})
		} else {
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	})
	mux.HandleFunc("/repos/owner/repo/issues/10", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghIssue(10, "Blocker 10", "open", []string{"in progress"}))
	})
	mux.HandleFunc("/repos/owner/repo/issues/20", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghIssue(20, "Blocker 20", "closed", []string{}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Len(t, issues[0].BlockedBy, 2)
	assert.Equal(t, "#10", *issues[0].BlockedBy[0].Identifier)
	assert.Equal(t, "#20", *issues[0].BlockedBy[1].Identifier)
	// States must be populated — dispatch enforcement depends on this
	require.NotNil(t, issues[0].BlockedBy[0].State, "blocker #10 state must be set")
	assert.Equal(t, "in progress", *issues[0].BlockedBy[0].State)
	require.NotNil(t, issues[0].BlockedBy[0].URL, "blocker #10 URL must be set")
	assert.Equal(t, "https://github.com/owner/repo/issues/10", *issues[0].BlockedBy[0].URL)
	require.NotNil(t, issues[0].BlockedBy[1].State, "blocker #20 state must be set")
	assert.Equal(t, "closed", *issues[0].BlockedBy[1].State)
	require.NotNil(t, issues[0].BlockedBy[1].URL, "blocker #20 URL must be set")
	assert.Equal(t, "https://github.com/owner/repo/issues/20", *issues[0].BlockedBy[1].URL)
}

// D4 strict fail-safe: GitHub 404 is AMBIGUOUS (deleted / transferred / access
// lost) — it must NOT resolve the blocker. State stays nil => blocked; the
// operator unblocks by removing the dangling reference from the issue body.
func TestGHBlockerState404LeavesStateUnknown(t *testing.T) {
	mux := http.NewServeMux()
	var listCalls atomic.Int32
	mux.HandleFunc("/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if listCalls.Add(1) == 1 {
			issue := ghIssue(1, "Blocked issue", "open", []string{"todo"})
			issue["body"] = "Blocked by #99"
			_ = json.NewEncoder(w).Encode([]interface{}{issue})
		} else {
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	})
	mux.HandleFunc("/repos/owner/repo/issues/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Len(t, issues[0].BlockedBy, 1)
	assert.Nil(t, issues[0].BlockedBy[0].State,
		"404 must leave blocker state unknown — GitHub 404s are ambiguous (permission loss, transfer)")
}

// D4 fail-safe: a transient blocker fetch error (network / rate-limit / 5xx)
// must NOT fabricate a terminal state. State stays nil so the orchestrator's
// unknown→blocked guard keeps the dependent issue out of dispatch.
func TestGHBlockerStateFetchErrorLeavesStateUnknown(t *testing.T) {
	mux := http.NewServeMux()
	var listCalls atomic.Int32
	mux.HandleFunc("/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if listCalls.Add(1) == 1 {
			issue := ghIssue(1, "Blocked issue", "open", []string{"todo"})
			issue["body"] = "Blocked by #77"
			_ = json.NewEncoder(w).Encode([]interface{}{issue})
		} else {
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	})
	mux.HandleFunc("/repos/owner/repo/issues/77", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Len(t, issues[0].BlockedBy, 1)
	assert.Nil(t, issues[0].BlockedBy[0].State,
		"transient fetch error must leave blocker state unknown (fail-safe), not fabricate closed")
}

func TestGHRateLimitsCaptured(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		_ = json.NewEncoder(w).Encode(ghIssue(42, "Issue 42", "open", []string{"todo"}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	limit0, _, _ := client.RateLimits()
	assert.Zero(t, limit0, "no rate limit observed yet")

	_, err := client.FetchIssueStatesByIDs(context.Background(), []string{"42"})
	require.NoError(t, err)

	limit, remaining, reset := client.RateLimits()
	assert.Equal(t, 5000, limit)
	assert.Equal(t, 4999, remaining)
	require.NotNil(t, reset)
	assert.Equal(t, int64(1700000000), reset.Unix())
}

// Ensure deduplicated blockers across issues only generate one fetch per unique ID.
func TestGHBlockerStateDeduplication(t *testing.T) {
	var mu sync.Mutex
	fetchCounts := map[string]int{}
	mux := http.NewServeMux()
	var listCalls atomic.Int32
	mux.HandleFunc("/repos/owner/repo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if listCalls.Add(1) == 1 {
			i1 := ghIssue(1, "Issue A", "open", []string{"todo"})
			i1["body"] = "Blocked by #5"
			i2 := ghIssue(2, "Issue B", "open", []string{"todo"})
			i2["body"] = "Blocked by #5" // same blocker
			_ = json.NewEncoder(w).Encode([]interface{}{i1, i2})
		} else {
			_ = json.NewEncoder(w).Encode([]interface{}{})
		}
	})
	mux.HandleFunc("/repos/owner/repo/issues/5", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetchCounts["5"]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ghIssue(5, "Blocker 5", "open", []string{"in progress"}))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 2)

	mu.Lock()
	count := fetchCounts["5"]
	mu.Unlock()
	assert.Equal(t, 1, count, "blocker #5 must be fetched exactly once despite two referencing issues")
}

func TestGHNon200FetchCandidateIssues(t *testing.T) {
	srv := newGHServer(t)
	srv.addResponse(nil, "", 401)
	ts := srv.serve()
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	_, err := client.FetchCandidateIssues(context.Background())
	require.Error(t, err)
	var apiErr *tracker.APIStatusError
	require.True(t, errors.As(err, &apiErr), "expected *tracker.APIStatusError, got %T: %v", err, err)
	assert.Equal(t, "github", apiErr.Adapter)
	assert.Equal(t, 401, apiErr.Status)
}

func TestGHIdentifierFormat(t *testing.T) {
	srv := newGHServer(t)
	srv.addResponse([]interface{}{ghIssue(42, "Issue 42", "open", []string{"todo"})}, "", 0)
	srv.addResponse([]interface{}{}, "", 0) // second active-state request ("in progress")
	ts := srv.serve()
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "42", issues[0].ID)
	assert.Equal(t, "#42", issues[0].Identifier)
}

func TestGHIssueOpenWithNoMatchingLabelNotEligible(t *testing.T) {
	srv := newGHServer(t)
	// Request for "todo": returns one eligible and one non-matching issue.
	srv.addResponse([]interface{}{
		ghIssue(1, "Issue 1", "open", []string{"todo"}),
		ghIssue(2, "Issue 2", "open", []string{"unrelated"}),
	}, "", 0)
	// Request for "in progress": empty.
	srv.addResponse([]interface{}{}, "", 0)
	ts := srv.serve()
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issues, err := client.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	// "unrelated" label → deriveState returns "" → filtered by fetchPaginated
	assert.Len(t, issues, 1)
	assert.Equal(t, "#1", issues[0].Identifier)
}

func TestGHFetchIssuesByStatesBacklogNotInActiveOrTerminal(t *testing.T) {
	// Regression: FetchIssuesByStates must return issues whose label is in
	// backlog_states even when that label is absent from active_states and
	// terminal_states (i.e. deriveState returns "").
	srv := newGHServer(t)
	// GitHub filters by label server-side; only the "backlog" issue is returned.
	srv.addResponse([]interface{}{
		ghIssue(10, "Backlog story", "open", []string{"backlog"}),
	}, "", 0)
	ts := srv.serve()
	defer ts.Close()

	client := ghclient.NewClient(ghclient.ClientConfig{
		APIKey:         "tok",
		ProjectSlug:    "owner/repo",
		ActiveStates:   []string{"todo"},
		TerminalStates: []string{"done"},
		Endpoint:       ts.URL,
	})
	issues, err := client.FetchIssuesByStates(context.Background(), []string{"backlog"})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal(t, "#10", issues[0].Identifier)
	assert.Equal(t, "backlog", issues[0].State)
}

func TestGHUpdateIssueStateRemovesBacklogLabel(t *testing.T) {
	// Regression: UpdateIssueState must DELETE backlog labels (not just
	// active+terminal) so dispatching from backlog removes "backlog".
	var mu sync.Mutex
	deleted := []string{}
	var addedLabel string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			// Path: /repos/owner/repo/issues/123/labels/<label>
			parts := splitPath(r.URL.Path)
			label := parts[len(parts)-1]
			mu.Lock()
			deleted = append(deleted, label)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if labels := body["labels"]; len(labels) > 0 {
				mu.Lock()
				addedLabel = labels[0]
				mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer ts.Close()

	client := ghclient.NewClient(ghclient.ClientConfig{
		APIKey:         "tok",
		ProjectSlug:    "owner/repo",
		ActiveStates:   []string{"todo", "in-progress"},
		TerminalStates: []string{"done", "cancelled"},
		BacklogStates:  []string{"backlog"},
		Endpoint:       ts.URL,
	})

	err := client.UpdateIssueState(context.Background(), "123", "todo")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "todo", addedLabel, "should add target label")
	assert.Contains(t, deleted, "in-progress", "should remove other active label")
	assert.Contains(t, deleted, "backlog", "should remove backlog label")
	assert.NotContains(t, deleted, "todo", "should not delete the target label itself")
}

// splitPath splits a URL path on "/" and returns non-empty parts.
func splitPath(p string) []string {
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

func TestGHMissingPageLinkError(t *testing.T) {
	_, err := ghclient.ParseNextLink("bad-link-header-with-no-rel-next")
	assert.ErrorIs(t, err, ghclient.ErrMissingPageLink)

	// Empty header = no next page, not an error
	url, err := ghclient.ParseNextLink("")
	assert.NoError(t, err)
	assert.Empty(t, url)
}

func TestGHCreateIssue(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody map[string]any

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(ghIssue(7, "Follow-up", "open", []string{"todo"}))
	}))
	defer ts.Close()

	client := ghclient.NewClient(defaultConfig(ts.URL))
	issue, err := client.CreateIssue(context.Background(), "123", "Follow-up", "Add regression coverage", "todo")
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/repos/owner/repo/issues", gotPath)
	assert.Equal(t, "Follow-up", gotBody["title"])
	assert.Equal(t, "Add regression coverage", gotBody["body"])
	assert.Equal(t, []any{"todo"}, gotBody["labels"])
	assert.Equal(t, "#7", issue.Identifier)
	assert.Equal(t, "todo", issue.State)
}

func TestGitHubCreateCommentWithKeyEmbedsMarker(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		gotBody = payload["body"]
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":99,"created_at":"2026-09-16T10:00:00Z"}`))
	}))
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	_, err := c.CreateCommentWithKey(context.Background(), "42", "k1", "hello")

	require.NoError(t, err)
	assert.Contains(t, gotBody, "hello")
	assert.Contains(t, gotBody, "<!-- itervox:ck:k1 -->")
}

func TestGitHubFindCommentByKeyAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	_, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.NoError(t, err)
	assert.False(t, found)
}

func TestGitHubFindCommentByKeyErrorIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	_, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.Error(t, err)
	assert.False(t, found)
}

// ghFindKeyScript configures newGHCommentsServer's fixture behavior for the
// FindCommentByKey tail-scan tests below.
type ghFindKeyScript struct {
	// hasKey is the page number carrying the marker comment; 0 = no page has it.
	hasKey int
	// lastPage is the rel="last" page number page 1's response advertises
	// via its Link header; 0 = no Link header at all.
	lastPage int
	// failPage, if nonzero, makes that page number return HTTP 500.
	failPage int
	// badLastLink, when true, makes page 1's Link header carry a rel="last"
	// entry whose page query parameter cannot be parsed.
	badLastLink bool
	// zeroLastPage, when true, makes page 1's Link header carry a rel="last"
	// entry whose page query parameter is "0" — syntactically parseable by
	// strconv.Atoi but not a legal page number.
	zeroLastPage bool
	// nextOnly, when true, makes page 1's Link header carry ONLY a
	// rel="next" entry — more pages exist, but their extent is unknown.
	nextOnly bool
}

// newGHCommentsServer starts an httptest server that plays back script and
// records every requested "page" query value (in request order) into the
// returned slice pointer, so tests can assert exactly which pages were
// fetched — the whole point of the ordering regression tests below.
func newGHCommentsServer(t *testing.T, script ghFindKeyScript) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var pages []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()

		pageNum, _ := strconv.Atoi(page)
		if script.failPage != 0 && pageNum == script.failPage {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if pageNum == 1 {
			switch {
			case script.badLastLink:
				w.Header().Set("Link", `<https://x/?page=abc>; rel="last"`)
			case script.nextOnly:
				w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues/42/comments?per_page=100&page=2>; rel="next"`, srv.URL))
			case script.zeroLastPage:
				w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues/42/comments?per_page=100&page=0>; rel="last"`, srv.URL))
			case script.lastPage > 0:
				w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/issues/42/comments?per_page=100&page=%d>; rel="last"`,
					srv.URL, script.lastPage))
			}
		}
		if script.hasKey != 0 && pageNum == script.hasKey {
			_, _ = w.Write([]byte(`[{"id":2,"body":"hi\n\n<!-- itervox:ck:k1 -->","created_at":"2026-09-16T10:00:00Z"}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":1,"body":"unrelated","created_at":"2026-09-16T10:00:00Z"}]`))
	}))
	return srv, &pages
}

func TestGitHubFindCommentByKeySinglePageFound(t *testing.T) {
	srv, pages := newGHCommentsServer(t, ghFindKeyScript{hasKey: 1})
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	got, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "2", got.ID)
	assert.Equal(t, []string{"1"}, *pages)
}

// TestGitHubFindCommentByKeyScansFromEnd is the regression test for the
// brief's page-ordering bug: GitHub orders comments by ascending ID with no
// sort/direction parameter, so page 1 is the OLDEST page. A key that only
// exists on the last page must still be found, and the scan must reach it
// by going straight to the last page rather than walking forward from 1.
func TestGitHubFindCommentByKeyScansFromEnd(t *testing.T) {
	srv, pages := newGHCommentsServer(t, ghFindKeyScript{hasKey: 5, lastPage: 5})
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	got, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "2", got.ID)
	assert.Equal(t, []string{"1", "5"}, *pages)
}

func TestGitHubFindCommentByKeyScansSecondToLastPage(t *testing.T) {
	srv, pages := newGHCommentsServer(t, ghFindKeyScript{hasKey: 4, lastPage: 5})
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	got, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "2", got.ID)
	assert.Equal(t, []string{"1", "5", "4"}, *pages)
}

func TestGitHubFindCommentByKeyTwoPagesDoesNotRefetchFirst(t *testing.T) {
	srv, pages := newGHCommentsServer(t, ghFindKeyScript{lastPage: 2})
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	got, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, got)
	assert.Equal(t, []string{"1", "2"}, *pages, "page 1 must not be fetched twice")
}

func TestGitHubFindCommentByKeyTailPageErrorIsUnknown(t *testing.T) {
	srv, _ := newGHCommentsServer(t, ghFindKeyScript{lastPage: 5, failPage: 5})
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	_, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.Error(t, err)
	assert.False(t, found)
}

func TestGitHubFindCommentByKeyUnparseableLastLinkIsUnknown(t *testing.T) {
	srv, pages := newGHCommentsServer(t, ghFindKeyScript{badLastLink: true})
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	_, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.Error(t, err)
	assert.False(t, found)
	assert.Equal(t, []string{"1"}, *pages, "an unparseable last link must not trigger further page fetches")
}

// TestGitHubFindCommentByKeyNonPositiveLastPageIsUnknown guards against a
// rel="last" page number that strconv.Atoi parses successfully (so it isn't
// caught by the "unparseable" path) but that isn't a legal page number
// (page=0 here). An implementation that accepts it as ok=true would then
// have its tail-scan loop's `page > 1` guard skip every page, silently
// collapsing an ambiguous response into "definitely absent".
func TestGitHubFindCommentByKeyNonPositiveLastPageIsUnknown(t *testing.T) {
	srv, pages := newGHCommentsServer(t, ghFindKeyScript{zeroLastPage: true})
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	_, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.Error(t, err)
	assert.False(t, found)
	assert.Equal(t, []string{"1"}, *pages, "a non-positive last page must not trigger further page fetches")
}

// TestGitHubFindCommentByKeyNextWithoutLastIsUnknown pins I2: a Link header
// that says more pages exist (rel="next") but omits rel="last" gives the
// tail scan nothing to bound itself by. Reporting "absent" there would let
// the flusher blind-post a duplicate of a comment sitting on a later page, so
// the answer must be an error ("unknown"), with no further pages fetched.
func TestGitHubFindCommentByKeyNextWithoutLastIsUnknown(t *testing.T) {
	srv, pages := newGHCommentsServer(t, ghFindKeyScript{nextOnly: true})
	defer srv.Close()

	c := ghclient.NewClient(defaultConfig(srv.URL))
	_, found, err := c.FindCommentByKey(context.Background(), "42", "k1")

	require.Error(t, err, `rel="next" without rel="last" is unknown, never a false not-found`)
	assert.False(t, found)
	assert.Equal(t, []string{"1"}, *pages, "only page 1 may be requested")
}

func TestParseLastPage(t *testing.T) {
	tests := []struct {
		name       string
		linkHeader string
		wantPage   int
		wantOK     bool
		wantErr    bool
	}{
		{name: "empty header", linkHeader: "", wantPage: 0, wantOK: false, wantErr: false},
		{name: "only rel=next", linkHeader: `<https://api.github.com/x?page=2>; rel="next"`, wantPage: 0, wantOK: false, wantErr: false},
		{name: "valid last page", linkHeader: `<https://api.github.com/x?page=7>; rel="last"`, wantPage: 7, wantOK: true, wantErr: false},
		{name: "non-numeric page", linkHeader: `<https://api.github.com/x?page=abc>; rel="last"`, wantPage: 0, wantOK: false, wantErr: true},
		{name: "zero page", linkHeader: `<https://api.github.com/x?page=0>; rel="last"`, wantPage: 0, wantOK: false, wantErr: true},
		{name: "negative page", linkHeader: `<https://api.github.com/x?page=-3>; rel="last"`, wantPage: 0, wantOK: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, ok, err := ghclient.ParseLastPage(tt.linkHeader)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantPage, page)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}
