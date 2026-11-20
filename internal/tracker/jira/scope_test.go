package jira_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

// fakeScopeSite answers /myself with the token's account "acc-me", the user
// lookup with "acc-ada" (any other id is 404), and every search with no
// issues.
func fakeScopeSite(_ int, r recorded, w http.ResponseWriter) {
	switch r.Path {
	case "/rest/api/3/myself":
		writeJSON(w, map[string]any{"accountId": "acc-me", "displayName": "Itervox Bot"})
	case "/rest/api/3/user":
		if r.Query != "accountId=acc-ada" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"accountId": "acc-ada", "displayName": "Ada Lovelace"})
	default:
		writeJSON(w, map[string]any{"issues": []any{}, "isLast": true})
	}
}

// searchJQL returns the jql of the only search request among reqs.
func searchJQL(t *testing.T, reqs []recorded) string {
	t.Helper()
	var jqls []string
	for _, r := range reqs {
		if r.Path == "/rest/api/3/search/jql" {
			jqls = append(jqls, r.jsonBody(t)["jql"].(string))
		}
	}
	require.Len(t, jqls, 1)
	return jqls[0]
}

func TestScopedJQLForEachCombination(t *testing.T) {
	const states = `status in ("To Do", "In Progress") ORDER BY created ASC`
	cases := []struct {
		name     string
		assignee string
		labels   []string
		want     string
	}{
		{"no scope", "", nil, `project = "PROJ" AND ` + states},
		{"assignee me", "me", nil, `project = "PROJ" AND assignee = currentUser() AND ` + states},
		{"assignee me in another case", " Me ", nil, `project = "PROJ" AND assignee = currentUser() AND ` + states},
		{"assignee account id", "557058:f58131cb-b67d", nil, `project = "PROJ" AND assignee = "557058:f58131cb-b67d" AND ` + states},
		{"one label", "", []string{"agent"}, `project = "PROJ" AND labels in ("agent") AND ` + states},
		{"labels cleaned", "", []string{" agent ", "", "Agent", "bot"}, `project = "PROJ" AND labels in ("agent", "bot") AND ` + states},
		{"blank labels only", "", []string{" ", ""}, `project = "PROJ" AND ` + states},
		{"assignee and labels", "me", []string{"agent", "bot"}, `project = "PROJ" AND assignee = currentUser() AND labels in ("agent", "bot") AND ` + states},
		{"account id and label", "acc-ada", []string{"agent"}, `project = "PROJ" AND assignee = "acc-ada" AND labels in ("agent") AND ` + states},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, seen := newScopedJiraServer(t, tc.assignee, tc.labels, fakeScopeSite)

			_, err := c.FetchCandidateIssues(t.Context())

			require.NoError(t, err)
			assert.Equal(t, tc.want, searchJQL(t, seen()))
		})
	}
}

func TestScopeQuotesHostileValues(t *testing.T) {
	c, seen := newScopedJiraServer(t, `x" OR assignee is EMPTY OR assignee = "y`, []string{`a" OR project is not EMPTY OR labels = "b`, `back\slash`}, fakeScopeSite)

	_, err := c.FetchCandidateIssues(t.Context())

	require.NoError(t, err)
	assert.Equal(t,
		`project = "PROJ" AND assignee = "x\" OR assignee is EMPTY OR assignee = \"y" AND labels in ("a\" OR project is not EMPTY OR labels = \"b", "back\\slash") AND status in ("To Do", "In Progress") ORDER BY created ASC`,
		searchJQL(t, seen()))
}

func TestNoScopeMakesNoAccountRequest(t *testing.T) {
	c, seen := newScopedJiraServer(t, "", nil, fakeScopeSite)

	_, err := c.FetchCandidateIssues(t.Context())

	require.NoError(t, err)
	reqs := seen()
	require.Len(t, reqs, 1, "only the search")
	assert.Equal(t, "/rest/api/3/search/jql", reqs[0].Path)
}

func TestFetchIssuesByStatesIsScoped(t *testing.T) {
	c, seen := newScopedJiraServer(t, "acc-ada", []string{"agent"}, fakeScopeSite)

	_, err := c.FetchIssuesByStates(t.Context(), []string{"Backlog", "Done"})

	require.NoError(t, err)
	assert.Equal(t, `project = "PROJ" AND assignee = "acc-ada" AND labels in ("agent") AND status in ("Backlog", "Done") ORDER BY created ASC`, searchJQL(t, seen()))
}

func TestAssigneeMeIsResolvedOnceBeforeTheFirstSearch(t *testing.T) {
	c, seen := newScopedJiraServer(t, "me", nil, fakeScopeSite)

	_, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	_, err = c.FetchIssuesByStates(t.Context(), []string{"Done"})
	require.NoError(t, err)

	var paths []string
	for _, r := range seen() {
		paths = append(paths, r.Method+" "+r.Path)
	}
	assert.Equal(t, []string{"GET /rest/api/3/myself", "POST /rest/api/3/search/jql", "POST /rest/api/3/search/jql"}, paths)
}

func TestAssigneeMeResolveFailureFailsTheReadWithoutSearching(t *testing.T) {
	c, seen := newScopedJiraServer(t, "me", nil, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	got, err := c.FetchCandidateIssues(t.Context())

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "jira_resolve_assignee")
	reqs := seen()
	require.Len(t, reqs, 1)
	assert.Equal(t, "/rest/api/3/myself", reqs[0].Path, "no search without the account")
}

func TestByIDReadsAreNotScoped(t *testing.T) {
	c, seen := newScopedJiraServer(t, "me", []string{"agent"}, func(_ int, r recorded, w http.ResponseWriter) {
		switch r.Path {
		case "/rest/api/3/issue/bulkfetch":
			// Neither issue is assigned to the token's account or labelled.
			writeJSON(w, map[string]any{"issues": []any{issueFixture("10001", "PROJ-1", "To Do"), issueFixture("10002", "OTHER-2", "To Do")}})
		case "/rest/api/3/issue/PROJ-1/comment":
			writeJSON(w, map[string]any{"comments": []any{}, "total": 0})
		default:
			writeJSON(w, issueFixture("10001", "PROJ-1", "To Do"))
		}
	})

	byIDs, err := c.FetchIssueStatesByIDs(t.Context(), []string{"10001", "10002"})
	require.NoError(t, err)
	assert.Len(t, byIDs, 2, "a by-ID read returns out-of-scope issues too")
	detail, err := c.FetchIssueByIdentifier(t.Context(), "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", detail.Identifier)

	for _, r := range seen() {
		assert.NotEqual(t, "/rest/api/3/myself", r.Path, "a by-ID read resolves no account")
		assert.NotEqual(t, "/rest/api/3/search/jql", r.Path, "a by-ID read runs no search")
		assert.NotContains(t, r.Body, "jql")
	}
}

func TestMatchesFilterChecksProjectAssigneeAndLabels(t *testing.T) {
	issue := func(key string, assignee string, labels ...string) domain.Issue {
		is := domain.Issue{ID: "1", Identifier: key, Labels: labels}
		if assignee != "" {
			is.Assignees = []string{assignee}
		}
		return is
	}
	type check struct {
		name         string
		issue        domain.Issue
		match, known bool
	}
	cases := []struct {
		name     string
		assignee string
		labels   []string
		checks   []check
	}{
		{"no scope: the project only", "", nil, []check{
			{"in the project", issue("PROJ-1", ""), true, true},
			{"moved to another project", issue("OTHER-1", ""), false, true},
			{"project key prefix only", issue("PROJECT-1", ""), false, true},
		}},
		{"labels", "", []string{"agent", "bot"}, []check{
			{"one of the labels", issue("PROJ-1", "", "bot"), true, true},
			{"label in another case", issue("PROJ-1", "", "AGENT"), true, true},
			{"no matching label", issue("PROJ-1", "", "other"), false, true},
			{"no label", issue("PROJ-1", ""), false, true},
			{"label but moved", issue("OTHER-1", "", "agent"), false, true},
		}},
		{"account id", "acc-ada", nil, []check{
			{"assigned to it", issue("PROJ-1", "acc-ada"), true, true},
			{"assigned to someone else", issue("PROJ-1", "acc-bob"), false, true},
			{"unassigned", issue("PROJ-1", ""), false, true},
		}},
		{"account id and labels", "acc-ada", []string{"agent"}, []check{
			{"both", issue("PROJ-1", "acc-ada", "agent"), true, true},
			{"assignee only", issue("PROJ-1", "acc-ada"), false, true},
			{"label only", issue("PROJ-1", "acc-bob", "agent"), false, true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, seen := newScopedJiraServer(t, tc.assignee, tc.labels, fakeScopeSite)
			for _, ck := range tc.checks {
				match, known := c.MatchesFilter(ck.issue)
				assert.Equal(t, ck.match, match, ck.name)
				assert.Equal(t, ck.known, known, ck.name)
			}
			assert.Empty(t, seen(), "MatchesFilter makes no request")
		})
	}
}

func TestMatchesFilterAssigneeMeIsUnknownUntilResolved(t *testing.T) {
	c, seen := newScopedJiraServer(t, "me", nil, fakeScopeSite)
	mine := domain.Issue{Identifier: "PROJ-1", Assignees: []string{"acc-me"}}
	theirs := domain.Issue{Identifier: "PROJ-2", Assignees: []string{"acc-ada"}}

	match, known := c.MatchesFilter(theirs)
	assert.False(t, match)
	assert.False(t, known, "before the account is read, no definitive answer")
	assert.Empty(t, seen())

	_, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)

	match, known = c.MatchesFilter(mine)
	assert.True(t, match)
	assert.True(t, known)
	match, known = c.MatchesFilter(theirs)
	assert.False(t, match)
	assert.True(t, known)
	match, known = c.MatchesFilter(domain.Issue{Identifier: "OTHER-1", Assignees: []string{"acc-me"}})
	assert.False(t, match, "moved out of the project")
	assert.True(t, known, "the project answers without the account")
}

func TestResolveAssignee(t *testing.T) {
	cases := []struct {
		name      string
		assignee  string
		wantID    string
		wantName  string
		wantErr   error
		wantQuery string
	}{
		{"me", "me", "acc-me", "Itervox Bot", nil, ""},
		{"account id", "acc-ada", "acc-ada", "Ada Lovelace", nil, "accountId=acc-ada"},
		{"unknown account id", "acc-nobody", "", "", tracker.ErrAssigneeNotFound, "accountId=acc-nobody"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, seen := newScopedJiraServer(t, tc.assignee, nil, fakeScopeSite)

			u, err := c.ResolveAssignee(t.Context())

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Contains(t, err.Error(), "jira_resolve_assignee")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantID, u.ID)
				assert.Equal(t, tc.wantName, u.Name)
			}
			reqs := seen()
			require.Len(t, reqs, 1)
			assert.Equal(t, tc.wantQuery, reqs[0].Query)
		})
	}

	t.Run("no assignee", func(t *testing.T) {
		c, seen := newJiraServer(t, fakeScopeSite)
		_, err := c.ResolveAssignee(t.Context())
		require.Error(t, err)
		assert.Empty(t, seen())
	})
}

var _ tracker.ScopeMatcher = (*jira.Client)(nil)
