package jira_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func commentFixture(id, created, text string) map[string]any {
	return map[string]any{
		"id":      id,
		"created": created,
		"author":  map[string]any{"accountId": "acc-" + id, "displayName": "Author " + id},
		"body": map[string]any{"type": "doc", "version": 1, "content": []any{
			map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": text}}},
		}},
	}
}

// detailHandler answers the issue GET, serves comments in the given pages
// (matched by startAt), and answers the property read with an empty
// properties list for every requested comment.
func detailHandler(issue map[string]any, total int, pages map[int][]any) func(int, recorded, http.ResponseWriter) {
	return func(_ int, r recorded, w http.ResponseWriter) {
		if r.Path == "/rest/api/3/comment/list" {
			var in struct {
				IDs []int64 `json:"ids"`
			}
			_ = json.Unmarshal([]byte(r.Body), &in)
			values := []any{}
			for _, id := range in.IDs {
				values = append(values, map[string]any{"id": strconv.FormatInt(id, 10), "properties": []any{}})
			}
			writeJSON(w, map[string]any{"isLast": true, "values": values})
			return
		}
		if strings.HasSuffix(r.Path, "/comment") {
			q, _ := url.ParseQuery(r.Query)
			start, _ := strconv.Atoi(q.Get("startAt"))
			writeJSON(w, map[string]any{"startAt": start, "maxResults": 100, "total": total, "comments": pages[start]})
			return
		}
		writeJSON(w, issue)
	}
}

func TestFetchIssueDetailReturnsCommentsOldestFirst(t *testing.T) {
	// Each page is served newest first; page 2 holds older comments than page 1.
	pages := map[int][]any{
		0: {commentFixture("4", "2024-01-04T00:00:00.000+0000", "d"), commentFixture("3", "2024-01-03T00:00:00.000+0000", "c")},
		2: {commentFixture("2", "2024-01-02T00:00:00.000+0000", "b"), commentFixture("1", "2024-01-01T00:00:00.000+0000", "a")},
	}
	c, seen := newJiraServer(t, detailHandler(issueFixture("10001", "PROJ-1", "To Do"), 4, pages))
	c.SetCommentPageSize(2)

	got, err := c.FetchIssueDetail(t.Context(), "10001")

	require.NoError(t, err)
	require.Len(t, got.Comments, 4)
	bodies := make([]string, 0, 4)
	for _, cm := range got.Comments {
		bodies = append(bodies, cm.Body)
	}
	assert.Equal(t, []string{"a", "b", "c", "d"}, bodies)
	assert.Equal(t, "acc-1", got.Comments[0].AuthorID)
	assert.Equal(t, "Author 1", got.Comments[0].AuthorName)
	reqs := seen()
	require.Len(t, reqs, 4, "issue GET, two comment pages, one property read")
	assert.Equal(t, "/rest/api/3/issue/10001", reqs[0].Path)
	q, _ := url.ParseQuery(reqs[1].Query)
	assert.Equal(t, "created", q.Get("orderBy"))
	assert.Equal(t, "0", q.Get("startAt"))
	assert.Equal(t, "2", q.Get("maxResults"))
	q, _ = url.ParseQuery(reqs[2].Query)
	assert.Equal(t, "2", q.Get("startAt"))
}

func TestFetchIssueDetailRequestsFieldsAndBranchProperty(t *testing.T) {
	c, seen := newJiraServer(t, detailHandler(issueFixture("10001", "PROJ-1", "To Do"), 0, nil))

	_, err := c.FetchIssueDetail(t.Context(), "10001")

	require.NoError(t, err)
	q, _ := url.ParseQuery(seen()[0].Query)
	assert.Equal(t, "summary,description,status,priority,labels,issuelinks,assignee,created,updated", q.Get("fields"))
	assert.Equal(t, "itervox.branch", q.Get("properties"))
}

func TestFetchIssueDetailReadsBranchProperty(t *testing.T) {
	cases := []struct {
		name  string
		props any
		want  string
	}{
		{"set", map[string]any{"itervox.branch": map[string]any{"branch": "feat/x"}}, "feat/x"},
		{"empty", map[string]any{"itervox.branch": map[string]any{"branch": ""}}, ""},
		{"absent", map[string]any{}, ""},
		{"no properties", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := issueFixture("10001", "PROJ-1", "To Do")
			if tc.props != nil {
				issue["properties"] = tc.props
			}
			c, _ := newJiraServer(t, detailHandler(issue, 0, nil))

			got, err := c.FetchIssueDetail(t.Context(), "10001")

			require.NoError(t, err)
			if tc.want == "" {
				assert.Nil(t, got.BranchName)
			} else if assert.NotNil(t, got.BranchName) {
				assert.Equal(t, tc.want, *got.BranchName)
			}
		})
	}
}

func TestFetchIssueDetailAcceptsKeyAndRejectsBadID(t *testing.T) {
	c, seen := newJiraServer(t, detailHandler(issueFixture("10001", "PROJ-1", "To Do"), 0, nil))

	_, err := c.FetchIssueDetail(t.Context(), "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "/rest/api/3/issue/PROJ-1", seen()[0].Path)

	_, err = c.FetchIssueDetail(t.Context(), "../x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jira_invalid_issue_id")
	assert.Len(t, seen(), 2, "bad id made no request (the two earlier requests are the key fetch)")
}

func TestFetchIssueByIdentifierRejectsMalformedKey(t *testing.T) {
	for _, key := range []string{"proj-1", "PROJ-", "PROJ-1/../../x", "PROJ-1 OR 1=1", ""} {
		t.Run(key, func(t *testing.T) {
			c, seen := newJiraServer(t, detailHandler(issueFixture("10001", "PROJ-1", "To Do"), 0, nil))

			_, err := c.FetchIssueByIdentifier(t.Context(), key)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "jira_invalid_issue_id")
			assert.Empty(t, seen())
		})
	}
}

func TestFetchIssueByIdentifierFetchesDetail(t *testing.T) {
	c, seen := newJiraServer(t, detailHandler(issueFixture("10001", "PROJ-1", "To Do"), 0, nil))

	got, err := c.FetchIssueByIdentifier(t.Context(), "PROJ-1")

	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", got.Identifier)
	assert.Equal(t, "/rest/api/3/issue/PROJ-1", seen()[0].Path)
}
