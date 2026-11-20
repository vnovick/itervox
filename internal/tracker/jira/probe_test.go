package jira_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

func TestProbeProjectAccessReturnsAccount(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		switch r.Path {
		case "/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"accountId":"abc","displayName":"Ada Lovelace"}`))
		case "/rest/api/3/project/PROJ":
			_, _ = w.Write([]byte(`{"key":"PROJ"}`))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	})
	account, err := c.ProbeProjectAccess(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "Ada Lovelace", account)
	reqs := seen()
	require.Len(t, reqs, 2)
	assert.Equal(t, "GET /rest/api/3/myself", reqs[0].Method+" "+reqs[0].Path)
	assert.Equal(t, "GET /rest/api/3/project/PROJ", reqs[1].Method+" "+reqs[1].Path)
}

func TestProbeProjectAccessReportsBadToken(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.ProbeProjectAccess(t.Context())
	var apiErr *tracker.APIStatusError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 401, apiErr.Status)
	assert.Contains(t, err.Error(), "jira_probe: ")
	assert.Len(t, seen(), 1, "no project request after a failed myself")
}

// TestProjectStatusesUnionsIssueTypes: Jira groups a project's statuses by
// issue type; ProjectStatuses returns each name once, in first-seen order,
// from one read-only GET.
func TestProjectStatusesUnionsIssueTypes(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		if r.Path != "/rest/api/3/project/PROJ/statuses" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"1","name":"Task","subtask":false,"statuses":[{"id":"10","name":"To Do"},{"id":"11","name":"In Progress"},{"id":"12","name":"Done"}]},
			{"id":"2","name":"Bug","subtask":false,"statuses":[{"id":"10","name":"To Do"},{"id":"13","name":"In Review"},{"id":"12","name":"Done"}]}
		]`))
	})
	got, err := c.ProjectStatuses(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"To Do", "In Progress", "Done", "In Review"}, got)
	reqs := seen()
	require.Len(t, reqs, 1)
	assert.Equal(t, "GET /rest/api/3/project/PROJ/statuses", reqs[0].Method+" "+reqs[0].Path)
	assert.Empty(t, reqs[0].Body, "a read sends no body")
}

func TestProjectStatusesReportsMissingProject(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.ProjectStatuses(t.Context())
	require.ErrorIs(t, err, tracker.ErrNotFound)
	assert.Contains(t, err.Error(), "jira_statuses: project PROJ")
}

func TestProjectStatusesRefusesEmptyProjectKey(t *testing.T) {
	c := jira.NewClient(jira.ClientConfig{Endpoint: "http://127.0.0.1:1", APIKey: "me@example.com:tok"})
	_, err := c.ProjectStatuses(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.project_slug is empty")
}

func TestProbeProjectAccessReportsMissingProject(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, r recorded, w http.ResponseWriter) {
		if r.Path == "/rest/api/3/myself" {
			_, _ = w.Write([]byte(`{"accountId":"abc"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.ProbeProjectAccess(t.Context())
	require.Error(t, err)
	assert.True(t, errors.Is(err, tracker.ErrNotFound))
}
