package jira_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

func TestSetStateListsUnderRace(t *testing.T) {
	c, _ := newJiraServer(t, emptySearch)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				c.SetStateLists([]string{fmt.Sprintf("S%d-%d", i, j)}, []string{"Done"})
			}
		})
		wg.Go(func() {
			for range 10 {
				_, err := c.FetchCandidateIssues(t.Context())
				assert.NoError(t, err)
			}
		})
	}
	wg.Wait()
}

func TestMalformedRefsNeverReachTheNetwork(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)
	ctx := t.Context()
	bad := []string{"", "PROJ-1\n", "1\n", "PROJ-1/../x", "PROJ-1?x=1", "PROJ-1#x", "１２３", "PROJ-１", "PROJ-1\x00", "%2e%2e", "PROJ-1 ", " 1", "proj-1", strings.Repeat("A", 70000) + "-1x"}
	for _, ref := range bad {
		_, err := c.FetchIssueDetail(ctx, ref)
		assert.Error(t, err, "%q", ref)
		_, err = c.FetchIssueStatesByIDs(ctx, []string{ref})
		assert.Error(t, err, "%q", ref)
		_, err = c.CreateComment(ctx, ref, "x")
		assert.Error(t, err, "%q", ref)
		_, err = c.CreateCommentWithKey(ctx, ref, "k", "x")
		assert.Error(t, err, "%q", ref)
		_, _, err = c.FindCommentByKey(ctx, ref, "k")
		assert.Error(t, err, "%q", ref)
		assert.Error(t, c.UpdateIssueState(ctx, ref, "Done"), "%q", ref)
		assert.Error(t, c.SetIssueBranch(ctx, ref, "b"), "%q", ref)
		_, err = c.FetchIssueByIdentifier(ctx, ref)
		assert.Error(t, err, "%q", ref)
	}
	assert.Empty(t, seen())
}

func TestJQLStatesCannotBreakOutOfQuotes(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)
	states := []string{`x" OR project != "PROJ`, `a\`, `\"`, "new\nline", "nul\x00", "ünï ✓", `" ORDER BY key --`}
	_, err := c.FetchIssuesByStates(t.Context(), states)
	require.NoError(t, err)
	jql := seen()[0].jsonBody(t)["jql"].(string)
	// Walk the JQL: outside quotes only the fixed skeleton may appear.
	var outside strings.Builder
	in, esc := false, false
	for _, r := range jql {
		switch {
		case esc:
			esc = false
		case in && r == '\\':
			esc = true
		case r == '"':
			in = !in
		case !in:
			outside.WriteRune(r)
		}
	}
	assert.Equal(t, "project =  AND status in (, , , , , , ) ORDER BY created ASC", outside.String())
}

func TestPaginationEdges(t *testing.T) {
	t.Run("isLast with a token stops", func(t *testing.T) {
		c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
			writeJSON(w, map[string]any{"issues": []any{issueFixture("1", "PROJ-1", "To Do")}, "nextPageToken": "t", "isLast": true})
		})
		got, err := c.FetchCandidateIssues(t.Context())
		require.NoError(t, err)
		assert.Len(t, got, 1)
		assert.Len(t, seen(), 1)
	})
	t.Run("error on page 2 returns no partial list", func(t *testing.T) {
		c, _ := newJiraServer(t, func(n int, _ recorded, w http.ResponseWriter) {
			if n == 1 {
				writeJSON(w, map[string]any{"issues": []any{issueFixture("1", "PROJ-1", "To Do")}, "nextPageToken": "t"})
				return
			}
			w.WriteHeader(http.StatusBadGateway)
		})
		got, err := c.FetchCandidateIssues(t.Context())
		require.Error(t, err)
		assert.Nil(t, got)
	})
	for _, n := range []int{1, 100, 101, 201} {
		t.Run(fmt.Sprintf("bulk %d ids", n), func(t *testing.T) {
			var sizes []int
			var mu sync.Mutex
			c, _ := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
				var b struct {
					IDs []string `json:"issueIdsOrKeys"`
				}
				_ = json.Unmarshal([]byte(r.Body), &b)
				mu.Lock()
				sizes = append(sizes, len(b.IDs))
				mu.Unlock()
				writeJSON(w, map[string]any{"issues": []any{}})
			})
			ids := make([]string, n)
			for i := range ids {
				ids[i] = fmt.Sprint(10000 + i)
			}
			got, err := c.FetchIssueStatesByIDs(t.Context(), ids)
			require.NoError(t, err)
			assert.NotNil(t, got)
			total := 0
			for _, s := range sizes {
				assert.LessOrEqual(t, s, 100)
				total += s
			}
			assert.Equal(t, n, total)
		})
	}
	t.Run("second chunk failing fails the call", func(t *testing.T) {
		c, _ := newJiraServer(t, func(n int, _ recorded, w http.ResponseWriter) {
			if n == 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, map[string]any{"issues": []any{}})
		})
		ids := make([]string, 150)
		for i := range ids {
			ids[i] = fmt.Sprint(10000 + i)
		}
		got, err := c.FetchIssueStatesByIDs(t.Context(), ids)
		require.Error(t, err)
		assert.Nil(t, got)
	})
}

func TestAPIKeyNeverInErrors(t *testing.T) {
	const key = "me@example.com:SUPERSECRET"
	cases := map[string]http.HandlerFunc{
		"401": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) },
		"500 body": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(r.Header.Get("Authorization")))
		},
		"html 200": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>" + r.Header.Get("Authorization")))
		},
		"issueErrs": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"issueErrors":[{"id":"1","errorMessage":"busy"}]}`))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			c := jira.NewClient(jira.ClientConfig{Endpoint: srv.URL, APIKey: key, ProjectSlug: "PROJ", ActiveStates: []string{"To Do"}})
			var errs []error
			_, err := c.FetchCandidateIssues(t.Context())
			errs = append(errs, err)
			_, err = c.FetchIssueStatesByIDs(t.Context(), []string{"1"})
			errs = append(errs, err)
			_, err = c.ProbeProjectAccess(t.Context())
			errs = append(errs, err)
			errs = append(errs, c.UpdateIssueState(t.Context(), "PROJ-1", "Done"))
			seenErr := 0
			for _, e := range errs {
				if e == nil {
					continue
				}
				seenErr++
				assert.NotContains(t, e.Error(), "SUPERSECRET")
				assert.NotContains(t, e.Error(), "bWVAZXhhbXBsZS5jb2")
			}
			assert.NotZero(t, seenErr)
		})
	}
	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		addr := srv.URL
		srv.Close()
		c := jira.NewClient(jira.ClientConfig{Endpoint: addr, APIKey: key, ProjectSlug: "PROJ"})
		_, err := c.ProbeProjectAccess(t.Context())
		require.Error(t, err)
		var apiErr *tracker.APIStatusError
		assert.False(t, errors.As(err, &apiErr))
		assert.NotContains(t, err.Error(), "SUPERSECRET")
	})
}
