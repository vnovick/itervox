package jira_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

// #114 — the incremental pull for the tracker store.

// sinceFixture is the since every incremental test asks from, and
// sinceWindowMs the epoch ms the query must carry: since less the 4-minute
// search-lag widening.
var (
	sinceFixture  = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	sinceWindowMs = sinceFixture.Add(-4 * time.Minute).UnixMilli()
)

// updatedSinceJQL is the exact query the incremental pull sends.
func updatedSinceJQL(ms int64) string {
	return fmt.Sprintf(`project = "PROJ" AND updated >= %d ORDER BY updated DESC`, ms)
}

// scopedIssue is an issue fixture with labels, an assignee and an update time.
func scopedIssue(id, key, status, assignee string, labels ...string) map[string]any {
	is := issueFixture(id, key, status)
	f := is["fields"].(map[string]any)
	f["updated"] = "2026-11-20T10:00:05.123+0000"
	ls := make([]any, 0, len(labels))
	for _, l := range labels {
		ls = append(ls, l)
	}
	f["labels"] = ls
	if assignee != "" {
		f["assignee"] = map[string]any{"accountId": assignee}
	}
	return is
}

func ids(issues []domain.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, is.ID)
	}
	return out
}

// TestFetchIssuesUpdatedSinceJQLForEachScope: the query is the project and
// the update time, whatever the assignee and labels; they are applied to
// the result in memory (TestFetchIssuesUpdatedSinceReportsScopeLeaversAsGone).
// The time is unquoted epoch milliseconds, so the token's time zone does
// not matter: the same instant in another zone gives the same query.
func TestFetchIssuesUpdatedSinceJQLForEachScope(t *testing.T) {
	cases := []struct {
		name     string
		assignee string
		labels   []string
	}{
		{"no scope", "", nil},
		{"assignee me", "me", nil},
		{"assignee account id", "acc-ada", nil},
		{"labels", "", []string{"agent", "bot"}},
		{"assignee me and labels", "me", []string{"agent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, seen := newScopedJiraServer(t, tc.assignee, tc.labels, fakeScopeSite)

			_, _, err := c.FetchIssuesUpdatedSince(t.Context(), sinceFixture.In(time.FixedZone("UTC+5:30", 19800)))

			require.NoError(t, err)
			assert.Equal(t, updatedSinceJQL(sinceWindowMs), searchJQL(t, seen()))
		})
	}
}

// TestFetchIssuesUpdatedSinceWidensTheWindowForSearchLag: Jira's search
// shows an update only after a delay, so the pull asks from 4 minutes
// before since, not from since.
func TestFetchIssuesUpdatedSinceWidensTheWindowForSearchLag(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)

	_, _, err := c.FetchIssuesUpdatedSince(t.Context(), sinceFixture)

	require.NoError(t, err)
	jql := searchJQL(t, seen())
	assert.Equal(t, updatedSinceJQL(sinceFixture.UnixMilli()-240_000), jql)
	assert.NotEqual(t, updatedSinceJQL(sinceFixture.UnixMilli()), jql)
}

// TestFetchIssuesUpdatedSinceReportsScopeLeaversAsGone: an updated issue the
// scope no longer covers (reassigned, label removed) is reported gone, so
// the store drops it at the next pull instead of the next full pull. The
// others are changed, in any status. With no scope nothing is gone.
func TestFetchIssuesUpdatedSinceReportsScopeLeaversAsGone(t *testing.T) {
	listing := []any{
		scopedIssue("1", "PROJ-1", "In Progress", "acc-me", "Agent"),
		scopedIssue("2", "PROJ-2", "Done", "acc-me", "agent"),
		scopedIssue("3", "PROJ-3", "To Do", "acc-ada", "agent"),
		scopedIssue("4", "PROJ-4", "To Do", "acc-me"),
		scopedIssue("5", "PROJ-5", "To Do", "", "agent"),
	}
	site := func(n int, r recorded, w http.ResponseWriter) {
		if r.Path == "/rest/api/3/search/jql" {
			writeJSON(w, map[string]any{"issues": listing, "isLast": true})
			return
		}
		fakeScopeSite(n, r, w)
	}

	scoped, _ := newScopedJiraServer(t, "me", []string{"agent"}, site)
	changed, gone, err := scoped.FetchIssuesUpdatedSince(t.Context(), sinceFixture)
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2"}, ids(changed), "in scope, any status")
	assert.Equal(t, []string{"3", "4", "5"}, gone, "reassigned, unlabelled, unassigned")

	byID, _ := newScopedJiraServer(t, "acc-ada", nil, site)
	changed, gone, err = byID.FetchIssuesUpdatedSince(t.Context(), sinceFixture)
	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, ids(changed))
	assert.Equal(t, []string{"1", "2", "4", "5"}, gone)

	unscoped, _ := newJiraServer(t, site)
	changed, gone, err = unscoped.FetchIssuesUpdatedSince(t.Context(), sinceFixture)
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2", "3", "4", "5"}, ids(changed))
	assert.Empty(t, gone)
}

// TestFetchIssuesUpdatedSinceFollowsPages: every page is read, with the
// first page's token, and the issues keep their update times.
func TestFetchIssuesUpdatedSinceFollowsPages(t *testing.T) {
	c, seen := newJiraServer(t, func(n int, _ recorded, w http.ResponseWriter) {
		if n == 1 {
			writeJSON(w, map[string]any{"issues": []any{scopedIssue("1", "PROJ-1", "To Do", "")}, "nextPageToken": "tok-2"})
			return
		}
		writeJSON(w, map[string]any{"issues": []any{scopedIssue("2", "PROJ-2", "Done", "")}, "isLast": true})
	})

	changed, gone, err := c.FetchIssuesUpdatedSince(t.Context(), sinceFixture)

	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2"}, ids(changed))
	assert.Empty(t, gone)
	require.NotNil(t, changed[1].UpdatedAt)
	assert.Equal(t, time.Date(2026, 11, 20, 10, 0, 5, 123e6, time.UTC), changed[1].UpdatedAt.UTC())
	reqs := seen()
	require.Len(t, reqs, 2)
	assert.Equal(t, "tok-2", reqs[1].jsonBody(t)["nextPageToken"])
	assert.Equal(t, updatedSinceJQL(sinceWindowMs), reqs[1].jsonBody(t)["jql"], "the same query on every page")
}

// TestFetchIssuesUpdatedSinceFailedPageIsAnError: a failed page fails the
// whole pull with nothing returned, never the pages read before it: the
// store keeps its cursor and retries.
func TestFetchIssuesUpdatedSinceFailedPageIsAnError(t *testing.T) {
	c, _ := newJiraServer(t, func(n int, _ recorded, w http.ResponseWriter) {
		if n == 1 {
			writeJSON(w, map[string]any{"issues": []any{scopedIssue("1", "PROJ-1", "To Do", "")}, "nextPageToken": "tok-2"})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})

	changed, gone, err := c.FetchIssuesUpdatedSince(t.Context(), sinceFixture)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "jira_search_updated")
	assert.Nil(t, changed)
	assert.Nil(t, gone)
}

// TestFetchIssuesUpdatedSinceEndlessPagesIsAnError: a server that never
// stops paging fails the pull after the page cap.
func TestFetchIssuesUpdatedSinceEndlessPagesIsAnError(t *testing.T) {
	c, seen := newJiraServer(t, func(n int, _ recorded, w http.ResponseWriter) {
		writeJSON(w, map[string]any{"issues": []any{scopedIssue(fmt.Sprint(n), fmt.Sprintf("PROJ-%d", n), "To Do", "")}, "nextPageToken": fmt.Sprintf("tok-%d", n+1)})
	})
	jira.SetMaxPages(c, 3)

	changed, gone, err := c.FetchIssuesUpdatedSince(t.Context(), sinceFixture)

	require.Error(t, err)
	assert.Nil(t, changed)
	assert.Nil(t, gone)
	assert.Len(t, seen(), 3)
}

// TestFetchIssuesUpdatedSinceAssigneeMeResolveFailure: with an assignee of
// "me" the token's account must be known to judge the scope; when it cannot
// be read the pull fails before any search.
func TestFetchIssuesUpdatedSinceAssigneeMeResolveFailure(t *testing.T) {
	c, seen := newScopedJiraServer(t, "me", nil, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	changed, gone, err := c.FetchIssuesUpdatedSince(t.Context(), sinceFixture)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "jira_resolve_assignee")
	assert.Nil(t, changed)
	assert.Nil(t, gone)
	reqs := seen()
	require.Len(t, reqs, 1)
	assert.Equal(t, "/rest/api/3/myself", reqs[0].Path)
}

// branchSite answers searches and bulk fetches with one issue that carries
// the itervox.branch property only when the request asks for it, as Jira
// does.
func branchSite(_ int, r recorded, w http.ResponseWriter) {
	var body map[string]any
	if r.Body != "" {
		body = map[string]any{}
		_ = json.Unmarshal([]byte(r.Body), &body)
	}
	is := scopedIssue("1", "PROJ-1", "To Do", "")
	if props, _ := body["properties"].([]any); len(props) == 1 && props[0] == "itervox.branch" {
		is["properties"] = map[string]any{"itervox.branch": map[string]any{"branch": "itervox/PROJ-1"}}
	}
	writeJSON(w, map[string]any{"issues": []any{is}, "isLast": true})
}

// TestListReadsCarryTheBranchProperty: the list reads (the candidate poll,
// a state list, the incremental pull) and the by-ID read ask for the
// itervox.branch property and return the branch, as the detail read does,
// so the tracker store does not drop the branch when it lists an issue
// again.
func TestListReadsCarryTheBranchProperty(t *testing.T) {
	c, seen := newJiraServer(t, branchSite)
	ctx := t.Context()

	cands, err := c.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	byState, err := c.FetchIssuesByStates(ctx, []string{"Done"})
	require.NoError(t, err)
	changed, _, err := c.FetchIssuesUpdatedSince(ctx, sinceFixture)
	require.NoError(t, err)
	byID, err := c.FetchIssueStatesByIDs(ctx, []string{"1"})
	require.NoError(t, err)

	for name, got := range map[string][]domain.Issue{"candidates": cands, "by states": byState, "updated since": changed, "by id": byID} {
		require.Len(t, got, 1, name)
		if assert.NotNil(t, got[0].BranchName, name) {
			assert.Equal(t, "itervox/PROJ-1", *got[0].BranchName, name)
		}
	}
	for _, r := range seen() {
		assert.Equal(t, []any{"itervox.branch"}, r.jsonBody(t)["properties"], r.Path)
	}
}

// TestRateLimitSnapshotStaysNilWithPerSecondHeaders: Jira's X-RateLimit
// headers count requests per second, not an hourly budget, so the adapter
// reports no snapshot even after a response that carries them; nil means
// "unknown" to every caller, which then never sheds polling reads.
func TestRateLimitSnapshotStaysNilWithPerSecondHeaders(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.Header().Set("X-RateLimit-Limit", "200")
		w.Header().Set("X-RateLimit-Remaining", "1")
		w.Header().Set("RateLimit", `"jira-burst-based";r=1;t=1`)
		writeJSON(w, map[string]any{"issues": []any{}, "isLast": true})
	})
	_, err := c.FetchCandidateIssues(t.Context())
	require.NoError(t, err)

	var rl tracker.RateLimiter = c
	assert.Nil(t, rl.RateLimitSnapshot())
}
