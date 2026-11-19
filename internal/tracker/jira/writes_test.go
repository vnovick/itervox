package jira_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

func TestTextToADFRoundTripsThroughADFToText(t *testing.T) {
	samples := []string{
		"one line",
		"first\nsecond",
		"first\n\nthird after blank line",
		"# Title\n\nSome `code` and {braces} here.\n\n- item {a}\n- item `b`",
		"trailing blanks\n\n\n",
		"  indented\n\ttabbed",
		"```go\nfmt.Println(\"{}\")\n```",
	}
	for _, s := range samples {
		got := jira.ADFToText(jira.TextToADF(s))
		assert.Equal(t, strings.TrimRight(s, " \t\r\n"), got, "sample %q", s)
	}
}

// A block of text is one paragraph. Lines inside a block are joined with hard
// breaks, and blank lines only separate blocks. One paragraph per line made
// every comment double-spaced in Jira.
func TestTextToADFMakesOneParagraphPerBlock(t *testing.T) {
	got, err := json.Marshal(jira.TextToADF("a\nb\n\n\nc"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[{"type":"text","text":"a"},{"type":"hardBreak"},{"type":"text","text":"b"}]},
		{"type":"paragraph","content":[{"type":"text","text":"c"}]}]}`, string(got))
	assert.Equal(t, "a\nb\n\nc", jira.ADFToText(jira.TextToADF("a\nb\n\n\nc")), "extra blank lines collapse to one")
}

func TestCreateCommentSendsADFAndReturnsComment(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, commentFixture("900", "2024-02-01T10:00:00.000+0000", "line one"))
	})

	got, err := c.CreateComment(t.Context(), "PROJ-1", "line one\n\nline three")

	require.NoError(t, err)
	reqs := seen()
	require.Len(t, reqs, 1)
	assert.Equal(t, "POST", reqs[0].Method)
	assert.Equal(t, "/rest/api/3/issue/PROJ-1/comment", reqs[0].Path)
	assert.JSONEq(t, `{"body":{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[{"type":"text","text":"line one"}]},
		{"type":"paragraph","content":[{"type":"text","text":"line three"}]}]}}`, reqs[0].Body)
	require.NotNil(t, got)
	assert.Equal(t, "900", got.ID)
	assert.Equal(t, "line one", got.Body)
	assert.Equal(t, "acc-900", got.AuthorID)
}

func TestCreateCommentRejectsEmptyBody(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)

	got, err := c.CreateComment(t.Context(), "PROJ-1", " \n\t ")

	require.Error(t, err)
	assert.Nil(t, got)
	assert.True(t, strings.HasPrefix(err.Error(), "jira_create_comment: empty body"), err.Error())
	assert.Empty(t, seen())
}

// transition follows IssueTransition: id, name, and to.name.
func transition(id, name, to string) map[string]any {
	return map[string]any{"id": id, "name": name, "to": map[string]any{"name": to}}
}

func transitionsHandler(ts ...map[string]any) func(int, recorded, http.ResponseWriter) {
	return func(_ int, r recorded, w http.ResponseWriter) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.Path, "/transitions"):
			writeJSON(w, map[string]any{"transitions": ts})
		case r.Method == "POST" && strings.HasSuffix(r.Path, "/transitions"):
			w.WriteHeader(http.StatusNoContent)
		default: // GET issue for fields=status
			writeJSON(w, issueFixture("10001", "PROJ-1", "In Progress"))
		}
	}
}

func TestUpdateIssueStateResolvesTransitionByTargetName(t *testing.T) {
	c, seen := newJiraServer(t, transitionsHandler(
		transition("11", "Start", "In Progress"),
		transition("31", "Finish", "Done"),
		transition("32", "Finish again", "Done"),
	))

	err := c.UpdateIssueState(t.Context(), "PROJ-1", "done")

	require.NoError(t, err)
	reqs := seen()
	require.Len(t, reqs, 3, "status GET, transitions GET, transition POST")
	assert.Equal(t, "GET /rest/api/3/issue/PROJ-1?fields=status", reqs[0].Method+" "+reqs[0].Path+"?"+reqs[0].Query)
	assert.Equal(t, "GET", reqs[1].Method)
	assert.Equal(t, "/rest/api/3/issue/PROJ-1/transitions", reqs[1].Path)
	assert.Equal(t, "POST", reqs[2].Method)
	assert.Equal(t, "/rest/api/3/issue/PROJ-1/transitions", reqs[2].Path)
	assert.JSONEq(t, `{"transition":{"id":"31"}}`, reqs[2].Body)
}

func TestUpdateIssueStateNoopWhenAlreadyInState(t *testing.T) {
	c, seen := newJiraServer(t, transitionsHandler(transition("31", "Finish", "Done")))

	err := c.UpdateIssueState(t.Context(), "PROJ-1", "in progress")

	require.NoError(t, err)
	for _, r := range seen() {
		assert.NotEqual(t, "POST", r.Method, "no POST when the issue is already in the state")
	}
	require.Len(t, seen(), 1, "one status GET and nothing else")
	assert.Equal(t, "/rest/api/3/issue/PROJ-1", seen()[0].Path)
	assert.Equal(t, "fields=status", seen()[0].Query)
}

func TestUpdateIssueStateSendsNothingWhenAlreadyInStatus(t *testing.T) {
	c, seen := newJiraServer(t, transitionsHandler( // issue is "In Progress"
		transition("21", "Keep working", "In Progress"),
		transition("31", "Finish", "Done"),
	))

	err := c.UpdateIssueState(t.Context(), "PROJ-1", "In Progress")

	require.NoError(t, err)
	reqs := seen()
	require.Len(t, reqs, 1, "one GET, even when a looped transition exists")
	assert.Equal(t, "GET", reqs[0].Method)
}

func TestUpdateIssueStateErrorsWhenNoTransition(t *testing.T) {
	c, seen := newJiraServer(t, transitionsHandler(
		transition("11", "Start", "In Progress"),
		transition("31", "Finish", "Done"),
	))

	err := c.UpdateIssueState(t.Context(), "PROJ-1", "Blocked")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `jira_update_state: no transition to "Blocked" from "In Progress"`)
	assert.Contains(t, err.Error(), "(reachable: Done, In Progress)")
	reqs := seen()
	require.Len(t, reqs, 2, "status GET, then transitions GET")
	assert.Equal(t, "fields=status", reqs[0].Query)
	assert.Equal(t, "/rest/api/3/issue/PROJ-1/transitions", reqs[1].Path)
	assert.Equal(t, "fields=status", reqs[0].Query)
	assert.Equal(t, "/rest/api/3/issue/PROJ-1/transitions", reqs[1].Path)
	for _, r := range reqs {
		assert.Equal(t, "GET", r.Method)
	}
}

func TestSetIssueBranchWritesIssueProperty(t *testing.T) {
	c, seen := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		w.WriteHeader(http.StatusCreated) // empty body
	})

	err := c.SetIssueBranch(t.Context(), "PROJ-1", "feat/proj-1")

	require.NoError(t, err)
	reqs := seen()
	require.Len(t, reqs, 1)
	assert.Equal(t, "PUT", reqs[0].Method)
	assert.Equal(t, "/rest/api/3/issue/PROJ-1/properties/itervox.branch", reqs[0].Path)
	assert.JSONEq(t, `{"branch":"feat/proj-1"}`, reqs[0].Body)
}

// createIssueHandler serves the endpoints CreateIssue calls. The source issue
// has the given issuetype; the new issue is 10050 / PROJ-13.
func createIssueHandler(issuetype map[string]any) func(int, recorded, http.ResponseWriter) {
	return func(_ int, r recorded, w http.ResponseWriter) {
		switch {
		case r.Method == "GET" && r.Query == "fields=issuetype":
			writeJSON(w, map[string]any{"id": "10001", "key": "PROJ-1", "fields": map[string]any{"issuetype": issuetype}})
		case r.Method == "POST" && r.Path == "/rest/api/3/issue":
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"id": "10050", "key": "PROJ-13"})
		case r.Method == "GET" && r.Query == "fields=status":
			writeJSON(w, issueFixture("10050", "PROJ-13", "To Do"))
		case r.Method == "GET" && strings.HasSuffix(r.Path, "/transitions"):
			writeJSON(w, map[string]any{"transitions": []any{transition("31", "Finish", "Done")}})
		case r.Method == "POST" && strings.HasSuffix(r.Path, "/transitions"):
			w.WriteHeader(http.StatusNoContent)
		case r.Path == "/rest/api/3/issue/bulkfetch":
			writeJSON(w, map[string]any{"issues": []any{issueFixture("10050", "PROJ-13", "Done")}})
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}
}

func TestCreateIssueCopiesTypeLinksBlockerAndMovesState(t *testing.T) {
	c, seen := newJiraServer(t, createIssueHandler(map[string]any{"id": "10002", "name": "Story", "subtask": false}))

	got, err := c.CreateIssue(t.Context(), "PROJ-1", tracker.NewIssue{
		Title: "Follow up", Body: "Do it\n\nnow", State: "Done",
		Labels: []string{"a", " A ", "", "b "}, BlockedBySource: true,
	})

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "PROJ-13", got.Identifier)
	assert.Equal(t, "Done", got.State)
	reqs := seen()
	require.Len(t, reqs, 6)
	assert.Equal(t, "GET /rest/api/3/issue/PROJ-1?fields=issuetype", reqs[0].Method+" "+reqs[0].Path+"?"+reqs[0].Query)
	assert.Equal(t, "POST /rest/api/3/issue", reqs[1].Method+" "+reqs[1].Path)
	assert.JSONEq(t, `{"fields":{"project":{"key":"PROJ"},"summary":"Follow up","issuetype":{"id":"10002"},
		"labels":["a","b"],
		"description":{"type":"doc","version":1,"content":[
			{"type":"paragraph","content":[{"type":"text","text":"Do it"}]},
			{"type":"paragraph","content":[{"type":"text","text":"now"}]}]}},
		"update":{"issuelinks":[{"add":{"type":{"name":"Blocks"},"inwardIssue":{"key":"PROJ-1"}}}]}}`, reqs[1].Body)
	assert.Equal(t, "GET /rest/api/3/issue/10050?fields=status", reqs[2].Method+" "+reqs[2].Path+"?"+reqs[2].Query)
	assert.Equal(t, "GET /rest/api/3/issue/10050/transitions", reqs[3].Method+" "+reqs[3].Path)
	assert.Equal(t, "POST /rest/api/3/issue/10050/transitions", reqs[4].Method+" "+reqs[4].Path)
	assert.JSONEq(t, `{"transition":{"id":"31"}}`, reqs[4].Body)
	assert.Equal(t, "POST /rest/api/3/issue/bulkfetch", reqs[5].Method+" "+reqs[5].Path)
	for _, r := range reqs {
		assert.NotEqual(t, "/rest/api/3/issueLink", r.Path, "the blocker is part of the create request")
	}
	assert.Equal(t, []any{"10050"}, reqs[5].jsonBody(t)["issueIdsOrKeys"])
}

func TestCreateIssueUsesTaskForSubtaskSource(t *testing.T) {
	c, seen := newJiraServer(t, createIssueHandler(map[string]any{"id": "10003", "name": "Sub-task", "subtask": true}))

	got, err := c.CreateIssue(t.Context(), "PROJ-1", tracker.NewIssue{Title: "Follow up"})

	require.NoError(t, err)
	require.NotNil(t, got)
	reqs := seen()
	require.Len(t, reqs, 3, "type lookup, create, read-back")
	assert.JSONEq(t, `{"fields":{"project":{"key":"PROJ"},"summary":"Follow up","issuetype":{"name":"Task"}}}`, reqs[1].Body)
}

func TestWritesRejectInvalidIssueID(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)
	ctx := t.Context()
	cases := map[string]func() error{
		"CreateComment":    func() error { _, err := c.CreateComment(ctx, "bad id", "hi"); return err },
		"UpdateIssueState": func() error { return c.UpdateIssueState(ctx, "../x", "Done") },
		"SetIssueBranch":   func() error { return c.SetIssueBranch(ctx, "proj-1", "b") },
		"CreateIssue": func() error {
			_, err := c.CreateIssue(ctx, "PROJ 1", tracker.NewIssue{Title: "t"})
			return err
		},
	}
	for name, call := range cases {
		err := call()
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "jira_invalid_issue_id", name)
	}
	assert.Empty(t, seen())
}

func TestFetchIssueStatesByIDsFailsOnIssueErrors(t *testing.T) {
	c, _ := newJiraServer(t, func(_ int, _ recorded, w http.ResponseWriter) {
		writeJSON(w, map[string]any{
			"issues": []any{issueFixture("10001", "PROJ-1", "To Do")},
			"issueErrors": []any{
				map[string]any{"id": "10002", "errorMessage": "temporarily unavailable"},
				map[string]any{"id": "10003", "errorMessage": "payload too large"},
			},
		})
	})

	got, err := c.FetchIssueStatesByIDs(t.Context(), []string{"10001", "10002", "10003"})

	require.Error(t, err)
	assert.Nil(t, got)
	assert.True(t, strings.HasPrefix(err.Error(), "jira_bulk_fetch: "), err.Error())
	assert.Contains(t, err.Error(), "2 issue(s)")
	assert.Contains(t, err.Error(), "temporarily unavailable")
}

func TestFetchIssueByIdentifierRefusesOtherProject(t *testing.T) {
	c, seen := newJiraServer(t, emptySearch)

	got, err := c.FetchIssueByIdentifier(t.Context(), "OTHER-12")

	require.Error(t, err)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, tracker.ErrNotFound)
	assert.Empty(t, seen())
}
