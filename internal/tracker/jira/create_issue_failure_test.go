package jira_test

import (
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

// fakeJira is a small stateful Jira: it stores created issues with a status
// and answers search with every stored issue. New issues start in the
// workflow's initial status, "To Do", as Jira does, and from "To Do" only
// "Start" to "In Progress" exists.
type fakeJira struct {
	mu     sync.Mutex
	issues map[string]map[string]any // id -> issue
	nextID int
	reqs   []string
}

func newFakeJira() *fakeJira {
	return &fakeJira{issues: map[string]map[string]any{}, nextID: 10050}
}

func (f *fakeJira) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "POST" && r.URL.Path == "/rest/api/3/issue":
		id := fmt.Sprint(f.nextID)
		key := fmt.Sprintf("PROJ-%d", f.nextID-10037)
		f.nextID++
		f.issues[id] = issueFixture(id, key, "To Do")
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": id, "key": key})
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
		delete(f.issues, strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/transitions"):
		writeJSON(w, map[string]any{"transitions": []any{transition("11", "Start", "In Progress")}})
	case r.Method == "GET" && r.URL.Query().Get("fields") == "status":
		writeJSON(w, f.issues[strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")])
	case r.Method == "POST" && r.URL.Path == "/rest/api/3/search/jql":
		var out []any
		for _, is := range f.issues {
			out = append(out, is)
		}
		writeJSON(w, map[string]any{"issues": out, "isLast": true})
	default:
		w.WriteHeader(http.StatusTeapot)
	}
}

func (f *fakeJira) client(t *testing.T) *jira.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return jira.NewClient(jira.ClientConfig{
		Endpoint: srv.URL, APIKey: "me@example.com:tok", ProjectSlug: "PROJ",
		ActiveStates: []string{"To Do", "In Progress"},
	})
}

func TestCreateIssueRejectedCreateLeavesNothing(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		if r.Method == "GET" {
			writeJSON(w, map[string]any{"fields": map[string]any{"issuetype": map[string]any{"id": "10002", "name": "Story"}}})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})

	got, err := c.CreateIssue(t.Context(), "PROJ-1", tracker.NewIssue{Title: "Follow up", State: "Done", BlockedBySource: true})

	require.Error(t, err)
	assert.Nil(t, got)
	assert.True(t, strings.HasPrefix(err.Error(), "jira_create_issue: "), err.Error())
	reqs := seen()
	require.Len(t, reqs, 2, "type lookup and one create, nothing else")
	assert.Equal(t, "GET", reqs[0].Method)
	assert.Equal(t, "POST /rest/api/3/issue", reqs[1].Method+" "+reqs[1].Path)
}

func TestCreateIssueDeletesTheIssueWhenTheStateMoveFails(t *testing.T) {
	f := newFakeJira()
	c := f.client(t)

	got, err := c.CreateIssue(t.Context(), "", tracker.NewIssue{Title: "Follow up", State: "Backlog"})

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), `jira_create_issue: could not move PROJ-13 to "Backlog"; the new issue was deleted: `)
	assert.Contains(t, err.Error(), "no transition to")
	candidates, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Empty(t, candidates, "a failed CreateIssue must not leave an issue that the next poll dispatches")
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Contains(t, f.reqs, "DELETE /rest/api/3/issue/10050")
}

func TestCreateIssueReportsKeyWhenDeleteAlsoFails(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		switch {
		case r.Method == "POST" && r.Path == "/rest/api/3/issue":
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"id": "10050", "key": "PROJ-13"})
		case r.Method == "GET" && r.Query == "fields=status":
			writeJSON(w, issueFixture("10050", "PROJ-13", "To Do"))
		case r.Method == "GET" && strings.HasSuffix(r.Path, "/transitions"):
			writeJSON(w, map[string]any{"transitions": []any{transition("11", "Start", "In Progress")}})
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	})

	got, err := c.CreateIssue(t.Context(), "", tracker.NewIssue{Title: "Follow up", State: "Backlog"})

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), `jira_create_issue: created PROJ-13 but could not move it to "Backlog" and could not delete it: `)
	assert.Contains(t, err.Error(), "; delete: ")
	var apiErr *tracker.APIStatusError
	require.ErrorAs(t, err, &apiErr, "the delete cause stays inspectable")
	assert.Equal(t, http.StatusForbidden, apiErr.Status)
}
