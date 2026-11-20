package jira_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

// recorded is one request the test server saw.
type recorded struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// jsonBody decodes the recorded body into a map.
func (r recorded) jsonBody(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.Body), &m))
	return m
}

// newJiraServer starts a test server that records every request and answers
// through handler. It returns the recorder accessor and the client.
func newJiraServer(t *testing.T, handler func(n int, r recorded, w http.ResponseWriter)) (*jira.Client, func() []recorded) {
	t.Helper()
	return newScopedJiraServer(t, "", nil, handler)
}

// newScopedJiraServer is newJiraServer with a client scoped by assignee and
// labels.
func newScopedJiraServer(t *testing.T, assignee string, labels []string, handler func(n int, r recorded, w http.ResponseWriter)) (*jira.Client, func() []recorded) {
	t.Helper()
	var mu sync.Mutex
	var seen []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec := recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: string(b)}
		mu.Lock()
		seen = append(seen, rec)
		n := len(seen)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		handler(n, rec, w)
	}))
	t.Cleanup(srv.Close)
	c := jira.NewClient(jira.ClientConfig{
		Endpoint:     srv.URL + "/",
		APIKey:       "me@example.com:tok",
		ProjectSlug:  "PROJ",
		ActiveStates: []string{"To Do", "In Progress"},
		Assignee:     assignee,
		Labels:       labels,
	})
	return c, func() []recorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]recorded(nil), seen...)
	}
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

// issueFixture follows the IssueBean schema: id, key, fields.
func issueFixture(id, key, status string) map[string]any {
	return map[string]any{
		"id":  id,
		"key": key,
		"fields": map[string]any{
			"summary": "Summary " + key,
			"status":  map[string]any{"name": status},
		},
	}
}
