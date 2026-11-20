package jira_test

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

func emptySearch(_ int, _ recorded, w http.ResponseWriter) {
	writeJSON(w, map[string]any{"issues": []any{}, "isLast": true})
}

func TestFetchCandidateIssuesBuildsBoundedJQL(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)

	_, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)

	reqs := seen()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodPost, reqs[0].Method)
	body := reqs[0].jsonBody(t)
	assert.Equal(t, `project = "PROJ" AND status in ("To Do", "In Progress") ORDER BY created ASC`, body["jql"])
	assert.Equal(t, []any{"summary", "description", "status", "priority", "labels", "issuelinks", "assignee", "created", "updated"}, body["fields"])
	assert.EqualValues(t, 100, body["maxResults"])
	assert.NotContains(t, body, "nextPageToken", "first page carries no token")
}

func TestJQLQuotingEscapesQuotesAndBackslashes(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)

	_, err := c.FetchIssuesByStates(t.Context(), []string{`Say "hi" \ bye`})
	require.NoError(t, err)

	jql := seen()[0].jsonBody(t)["jql"]
	assert.Equal(t, `project = "PROJ" AND status in ("Say \"hi\" \\ bye") ORDER BY created ASC`, jql)
}

func TestFetchIssuesByStatesEmptyMakesNoRequest(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)

	got, err := c.FetchIssuesByStates(t.Context(), nil)

	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
	assert.Empty(t, seen())
}

func TestSetStateListsChangesCandidateQuery(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)
	c.SetStateLists([]string{"Review"}, []string{"Closed"})

	_, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)

	assert.Equal(t, `project = "PROJ" AND status in ("Review") ORDER BY created ASC`, seen()[0].jsonBody(t)["jql"])

	c.SetStateLists(nil, nil)
	got, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Len(t, seen(), 1, "no active states: no request")
}

func TestSearchFollowsNextPageToken(t *testing.T) {
	c, seen := newJiraServer(t, func(n int, _ recorded, w http.ResponseWriter) {
		if n == 1 {
			writeJSON(w, map[string]any{
				"issues":        []any{issueFixture("10001", "PROJ-1", "To Do"), issueFixture("10002", "PROJ-2", "To Do")},
				"nextPageToken": "tok-2",
				"isLast":        false,
			})
			return
		}
		writeJSON(w, map[string]any{"issues": []any{issueFixture("10003", "PROJ-3", "To Do")}, "isLast": true})
	})

	got, err := c.FetchCandidateIssues(t.Context())

	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"PROJ-1", "PROJ-2", "PROJ-3"}, []string{got[0].Identifier, got[1].Identifier, got[2].Identifier})
	reqs := seen()
	require.Len(t, reqs, 2)
	assert.Equal(t, "tok-2", reqs[1].jsonBody(t)["nextPageToken"])
	assert.Equal(t, reqs[0].jsonBody(t)["jql"], reqs[1].jsonBody(t)["jql"])
}

func TestSearchStopsWhenPaginationNeverEnds(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		writeJSON(w, map[string]any{"issues": []any{}, "nextPageToken": "again", "isLast": false})
	})
	jira.SetMaxPages(c, 3)

	_, err := c.FetchCandidateIssues(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "jira_search: pagination did not end")
	assert.Len(t, seen(), 3)
}

func TestFetchIssueStatesByIDsUsesBulkFetch(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		// 10002 was deleted: the response omits it.
		writeJSON(w, map[string]any{
			"issues":      []any{issueFixture("10001", "PROJ-1", "Done"), issueFixture("10003", "PROJ-3", "To Do")},
			"issueErrors": []any{},
		})
	})

	got, err := c.FetchIssueStatesByIDs(t.Context(), []string{"10001", "10002", "10003"})

	require.NoError(t, err)
	require.Len(t, got, 2, "a missing id is skipped, not an error")
	assert.Equal(t, "10001", got[0].ID)
	assert.Equal(t, "Done", got[0].State)
	assert.Equal(t, "10003", got[1].ID)
	reqs := seen()
	require.Len(t, reqs, 1)
	assert.Equal(t, http.MethodPost, reqs[0].Method)
	assert.Equal(t, "/rest/api/3/issue/bulkfetch", reqs[0].Path)
	body := reqs[0].jsonBody(t)
	assert.Equal(t, []any{"10001", "10002", "10003"}, body["issueIdsOrKeys"])
	assert.Len(t, body["fields"], 9)
}

func TestFetchIssueStatesByIDsEmptyMakesNoRequest(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)

	got, err := c.FetchIssueStatesByIDs(t.Context(), nil)

	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
	assert.Empty(t, seen())
}

func TestFetchIssueStatesByIDsChunksOf100(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		writeJSON(w, map[string]any{"issues": []any{}})
	})
	ids := make([]string, 150)
	for i := range ids {
		ids[i] = strconv.Itoa(10000 + i)
	}

	_, err := c.FetchIssueStatesByIDs(t.Context(), ids)

	require.NoError(t, err)
	reqs := seen()
	require.Len(t, reqs, 2)
	assert.Len(t, reqs[0].jsonBody(t)["issueIdsOrKeys"], 100)
	assert.Len(t, reqs[1].jsonBody(t)["issueIdsOrKeys"], 50)
}

func TestFetchIssueStatesByIDsRejectsNonNumericID(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)

	_, err := c.FetchIssueStatesByIDs(t.Context(), []string{"10001", "PROJ-1"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "jira_invalid_issue_id")
	assert.Empty(t, seen(), "validation happens before any request")
}
