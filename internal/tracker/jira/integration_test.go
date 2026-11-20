//go:build integration

package jira_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// envOr returns the environment variable key, or def when it is empty.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// splitCSV splits a comma list and trims each item.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// TestJiraLiveRoundTrip runs the adapter against a real Jira Cloud project.
// It needs JIRA_ENDPOINT, JIRA_PROJECT and JIRA_API_KEY (email:api_token) and
// skips without them. It creates two issues and moves both to the done state
// at the end. It never prints the API key.
func TestJiraLiveRoundTrip(t *testing.T) {
	endpoint := os.Getenv("JIRA_ENDPOINT")
	project := os.Getenv("JIRA_PROJECT")
	apiKey := os.Getenv("JIRA_API_KEY")
	if endpoint == "" || project == "" || apiKey == "" {
		t.Skip("set JIRA_ENDPOINT, JIRA_PROJECT and JIRA_API_KEY to run the live Jira round trip")
	}
	active := splitCSV(envOr("JIRA_ACTIVE_STATES", "To Do,In Progress"))
	working := envOr("JIRA_WORKING_STATE", "In Progress")
	done := envOr("JIRA_DONE_STATE", "Done")

	ctx := t.Context()
	c := jira.NewClient(jira.ClientConfig{
		Endpoint:     endpoint,
		APIKey:       apiKey,
		ProjectSlug:  project,
		ActiveStates: active,
	})

	// a. credentials and project scope.
	account, err := c.ProbeProjectAccess(ctx)
	require.NoError(t, err)
	t.Logf("probe ok, account: %s", account)

	// b. issue A, with a Markdown body and labels that need cleaning.
	stamp := time.Now().Unix()
	body := "line one\n\n- item\n\n```\ncode {x}\n```"
	a, err := c.CreateIssue(ctx, "", tracker.NewIssue{
		Title:  fmt.Sprintf("itervox integration %d blocker", stamp),
		Body:   body,
		Labels: []string{"itervox-integration", "Itervox-Integration", " "},
	})
	require.NoError(t, err)
	require.NotNil(t, a)
	// t.Context is already cancelled when a cleanup runs, so the cleanup
	// needs a context of its own.
	finish := func(id string) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.UpdateIssueState(cleanupCtx, id, done); err != nil {
			t.Logf("cleanup: move %s to %q: %v", id, done, err)
		}
	}
	t.Cleanup(func() { finish(a.ID) })
	assert.Regexp(t, regexp.MustCompile(`^[0-9]+$`), a.ID)
	assert.True(t, strings.HasPrefix(a.Identifier, project+"-"), "identifier %q", a.Identifier)
	assert.Len(t, a.Labels, 1)
	if a.Description != nil {
		assert.Equal(t, body, *a.Description)
	}

	// c. issue B, blocked by A.
	b, err := c.CreateIssue(ctx, a.ID, tracker.NewIssue{
		Title:           fmt.Sprintf("itervox integration %d blocked", stamp),
		BlockedBySource: true,
		State:           working,
	})
	require.NoError(t, err)
	require.NotNil(t, b)
	t.Logf("created %s and %s", a.Identifier, b.Identifier)
	t.Cleanup(func() { finish(b.ID) })
	assert.True(t, strings.EqualFold(b.State, working), "state %q, want %q", b.State, working)
	assertBlockedByA := func(is *domain.Issue) {
		t.Helper()
		require.Len(t, is.BlockedBy, 1)
		require.NotNil(t, is.BlockedBy[0].Identifier)
		assert.Equal(t, a.Identifier, *is.BlockedBy[0].Identifier)
		assert.NotNil(t, is.BlockedBy[0].State)
	}
	assertBlockedByA(b)

	// d. comments.
	second := "second comment\nwith `code` and {braces}"
	for _, text := range []string{"first comment", second} {
		cm, err := c.CreateComment(ctx, b.ID, text)
		require.NoError(t, err)
		require.NotNil(t, cm)
		assert.NotEmpty(t, cm.ID)
	}

	// e. branch.
	const branch = "itervox/integration-branch"
	require.NoError(t, c.SetIssueBranch(ctx, b.ID, branch))

	// f. detail.
	d, err := c.FetchIssueDetail(ctx, b.ID)
	require.NoError(t, err)
	require.Len(t, d.Comments, 2)
	assert.Equal(t, "first comment", d.Comments[0].Body)
	assert.Equal(t, second, d.Comments[1].Body)
	require.NotNil(t, d.BranchName)
	assert.Equal(t, branch, *d.BranchName)
	assertBlockedByA(d)

	// g. by identifier.
	byKey, err := c.FetchIssueByIdentifier(ctx, b.Identifier)
	require.NoError(t, err)
	assert.Equal(t, b.ID, byKey.ID)

	// h. states by id: the unknown id is dropped.
	states, err := c.FetchIssueStatesByIDs(ctx, []string{a.ID, b.ID, "999999999"})
	require.NoError(t, err)
	var gotIDs []string
	for _, is := range states {
		gotIDs = append(gotIDs, is.ID)
	}
	assert.ElementsMatch(t, []string{a.ID, b.ID}, gotIDs)

	// i. candidates: the search index can lag behind writes, so poll.
	deadline := time.Now().Add(30 * time.Second)
	for {
		cands, err := c.FetchCandidateIssues(ctx)
		require.NoError(t, err)
		seen := map[string]bool{}
		for _, is := range cands {
			seen[is.ID] = true
		}
		if seen[a.ID] && seen[b.ID] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("candidates still miss %s or %s after 30s", a.Identifier, b.Identifier)
		}
		time.Sleep(2 * time.Second)
	}

	// j. an unknown status has no transition.
	err = c.UpdateIssueState(ctx, b.ID, "No Such Status 12345")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no transition")

	// k. a comment over Jira's content limit is stored shortened, with the
	// note and the managed marker. Each line has an emoji, which Jira counts
	// as 2 characters.
	var long strings.Builder
	for i := range 400 {
		fmt.Fprintf(&long, "Step %d 🤖: ran the tests and changed the retry loop so that it stops after the deadline.\n\n", i)
	}
	longBody := tracker.MarkManagedComment(long.String())
	require.Greater(t, units(longBody), jiraLimit)
	cm, err := c.CreateComment(ctx, b.ID, longBody)
	require.NoError(t, err, "Jira refused the over-limit comment")
	require.True(t, tracker.IsManagedComment(*cm))
	d, err = c.FetchIssueDetail(ctx, b.ID)
	require.NoError(t, err)
	require.Len(t, d.Comments, 3)
	stored := d.Comments[2].Body
	assert.Equal(t, cm.Body, stored)
	assert.True(t, strings.HasPrefix(stored, "Step 0 🤖: ran the tests"), "stored text starts %q", stored[:40])
	assert.Contains(t, stored, "\n\n[Shortened by Itervox: ")
	assert.True(t, strings.HasSuffix(stored, "\n\n"+tracker.ManagedCommentMarker), "the marker is the last line")
	assert.NotContains(t, stored, "�")
	// The marker is a comment property; only the text counts for the limit.
	assert.LessOrEqual(t, units(strings.TrimSuffix(stored, "\n\n"+tracker.ManagedCommentMarker)), jiraLimit)
	t.Logf("over-limit comment: sent %d UTF-16 units, Jira stored %d; last line before the marker: %q",
		units(longBody), units(stored), stored[strings.LastIndex(stored, "\n\n[Shortened"):len(stored)-len(tracker.ManagedCommentMarker)-2])

	// l. keyed comment: a retry finds the first post and posts nothing.
	before, err := c.FetchIssueDetail(ctx, b.ID)
	require.NoError(t, err)
	key, err := outbox.NewCommentKey()
	require.NoError(t, err)
	_, found, err := c.FindCommentByKey(ctx, b.ID, key)
	require.NoError(t, err)
	assert.False(t, found, "a new key is on no comment")
	const keyedText = "keyed comment\nwith `code`"
	first, err := c.CreateCommentWithKey(ctx, b.ID, key, keyedText)
	require.NoError(t, err)
	// retry does what cmd/itervox deliverComment does on a retry: it looks
	// the key up first and posts only when the key is definitely absent.
	retry := func() *domain.Comment {
		t.Helper()
		got, found, err := c.FindCommentByKey(ctx, b.ID, key)
		require.NoError(t, err)
		if found {
			return got
		}
		cm, err := c.CreateCommentWithKey(ctx, b.ID, key, keyedText)
		require.NoError(t, err)
		return cm
	}
	again := retry()
	assert.Equal(t, first.ID, again.ID, "the retry found the first comment")
	after, err := c.FetchIssueDetail(ctx, b.ID)
	require.NoError(t, err)
	require.Len(t, after.Comments, len(before.Comments)+1, "one comment was added, not two")
	var keyed *domain.Comment
	for i := range after.Comments {
		if after.Comments[i].ID == first.ID {
			keyed = &after.Comments[i]
		}
	}
	require.NotNil(t, keyed, "the keyed comment is in the issue detail")
	assert.True(t, tracker.CommentHasKey(keyed.Body, key), "the orchestrator can match the keyed comment on read")
	assert.Equal(t, keyedText+"\n\n"+tracker.CommentKeyMarker(key), keyed.Body, "the text reads back as typed, with the marker added on read")
	t.Logf("keyed comment %s on %s", first.ID, b.Identifier)
}

// assignLive assigns the issue to the account through PUT
// /rest/api/3/issue/{id}/assignee. The adapter never assigns issues, so the
// test does it itself.
func assignLive(t *testing.T, endpoint, apiKey, issueID, accountID string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"accountId": accountID})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
		strings.TrimSuffix(endpoint, "/")+"/rest/api/3/issue/"+issueID+"/assignee", bytes.NewReader(body))
	require.NoError(t, err)
	email, token, _ := strings.Cut(apiKey, ":")
	req.SetBasicAuth(email, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "assign issue %s", issueID)
}

// TestJiraLiveScope proves the read scope on a real Jira Cloud project. It
// creates four issues: with and without a label of its own, and assigned to
// the token's account or unassigned. Scoped clients must list exactly the
// matching ones among them (the search index lags behind writes, so it
// polls), a by-ID read must stay unscoped, and MatchesFilter must agree with
// the search. It needs the same environment as TestJiraLiveRoundTrip and
// moves the issues to the done state at the end. It never prints the API key
// or the account id.
func TestJiraLiveScope(t *testing.T) {
	endpoint := os.Getenv("JIRA_ENDPOINT")
	project := os.Getenv("JIRA_PROJECT")
	apiKey := os.Getenv("JIRA_API_KEY")
	if endpoint == "" || project == "" || apiKey == "" {
		t.Skip("set JIRA_ENDPOINT, JIRA_PROJECT and JIRA_API_KEY to run the live Jira scope test")
	}
	active := splitCSV(envOr("JIRA_ACTIVE_STATES", "To Do,In Progress"))
	done := envOr("JIRA_DONE_STATE", "Done")
	ctx := t.Context()
	newClient := func(assignee string, labels ...string) *jira.Client {
		return jira.NewClient(jira.ClientConfig{
			Endpoint: endpoint, APIKey: apiKey, ProjectSlug: project, ActiveStates: active,
			Assignee: assignee, Labels: labels,
		})
	}
	base := newClient("")
	me, err := newClient("me").ResolveAssignee(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, me.ID)

	stamp := time.Now().Unix()
	label := fmt.Sprintf("itervox-scope-%d", stamp)
	specs := []struct {
		name           string
		labelled, mine bool
	}{
		{"labelled-mine", true, true},
		{"labelled-unassigned", true, false},
		{"plain-mine", false, true},
		{"plain-unassigned", false, false},
	}
	nameOf := map[string]string{} // issue id -> spec name
	var ids []string
	for _, s := range specs {
		in := tracker.NewIssue{Title: fmt.Sprintf("itervox scope %d %s", stamp, s.name)}
		if s.labelled {
			in.Labels = []string{label}
		}
		is, err := base.CreateIssue(ctx, "", in)
		require.NoError(t, err)
		id := is.ID
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := base.UpdateIssueState(cleanupCtx, id, done); err != nil {
				t.Logf("cleanup: move %s to %q: %v", id, done, err)
			}
		})
		if s.mine {
			assignLive(t, endpoint, apiKey, id, me.ID)
		}
		nameOf[id] = s.name
		ids = append(ids, id)
		t.Logf("created %s (%s)", is.Identifier, s.name)
	}

	meAndLabel := newClient("me", label)
	scopes := []struct {
		name   string
		client *jira.Client
		want   []string
	}{
		{"no scope", base, []string{"labelled-mine", "labelled-unassigned", "plain-mine", "plain-unassigned"}},
		{"labels", newClient("", label), []string{"labelled-mine", "labelled-unassigned"}},
		{"labels in upper case", newClient("", strings.ToUpper(label)), []string{"labelled-mine", "labelled-unassigned"}},
		{"assignee me", newClient("me"), []string{"labelled-mine", "plain-mine"}},
		{"assignee me and labels", meAndLabel, []string{"labelled-mine"}},
		{"assignee account id and labels", newClient(me.ID, label), []string{"labelled-mine"}},
	}
	// Only the issues this test created count: the project holds others.
	listed := func(c *jira.Client) []string {
		cands, err := c.FetchCandidateIssues(ctx)
		require.NoError(t, err)
		var names []string
		for _, is := range cands {
			if n, ok := nameOf[is.ID]; ok {
				names = append(names, n)
			}
		}
		slices.Sort(names)
		return names
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		got := map[string][]string{}
		var wrong []string
		for _, sc := range scopes {
			got[sc.name] = listed(sc.client)
			if !slices.Equal(got[sc.name], sc.want) {
				wrong = append(wrong, sc.name)
			}
		}
		if len(wrong) == 0 {
			for _, sc := range scopes {
				t.Logf("scope %-31s lists %v", sc.name+":", got[sc.name])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 90s these scopes still list the wrong issues: %v (got %v)", wrong, got)
		}
		time.Sleep(3 * time.Second)
	}

	// The by-ID read is not scoped; MatchesFilter agrees with the search.
	byID, err := meAndLabel.FetchIssueStatesByIDs(ctx, ids)
	require.NoError(t, err)
	require.Len(t, byID, len(ids), "a by-ID read returns every issue, in scope or not")
	for _, is := range byID {
		match, known := meAndLabel.MatchesFilter(is)
		assert.True(t, known, nameOf[is.ID])
		assert.Equal(t, nameOf[is.ID] == "labelled-mine", match, nameOf[is.ID])
		t.Logf("by id %s (%s): in scope %v", is.Identifier, nameOf[is.ID], match)
	}
}

// TestJiraLiveMarkdownRoundTrip posts every text in itervoxTexts, and
// everyConstruct, as comments on a new issue, reads them back through the
// adapter, and checks they are identical. It then reads the stored comments
// from Jira itself and checks that they hold real rich nodes, and that Jira
// renders them as HTML headings, lists and code. It needs the same
// environment as TestJiraLiveRoundTrip, logs the issue key, and moves the
// issue to the done state at the end.
func TestJiraLiveMarkdownRoundTrip(t *testing.T) {
	endpoint := os.Getenv("JIRA_ENDPOINT")
	project := os.Getenv("JIRA_PROJECT")
	apiKey := os.Getenv("JIRA_API_KEY")
	if endpoint == "" || project == "" || apiKey == "" {
		t.Skip("set JIRA_ENDPOINT, JIRA_PROJECT and JIRA_API_KEY to run the live Markdown round trip")
	}
	ctx := t.Context()
	c := jira.NewClient(jira.ClientConfig{Endpoint: endpoint, APIKey: apiKey, ProjectSlug: project})

	var description string
	for _, tc := range itervoxTexts {
		if strings.HasPrefix(tc.name, "verify-skill issue description") {
			description = tc.text
		}
	}
	require.NotEmpty(t, description)
	is, err := c.CreateIssue(ctx, "", tracker.NewIssue{
		Title: fmt.Sprintf("itervox Markdown round trip %d", time.Now().Unix()),
		Body:  description,
	})
	require.NoError(t, err)
	t.Logf("test issue: %s", is.Identifier)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.UpdateIssueState(cleanupCtx, is.ID, envOr("JIRA_DONE_STATE", "Done")); err != nil {
			t.Logf("cleanup: move %s to done: %v", is.Identifier, err)
		}
	})
	require.NotNil(t, is.Description)
	assert.Equal(t, description, *is.Description, "the description reads back exactly")

	var sent []string
	for _, tc := range itervoxTexts {
		sent = append(sent, strings.TrimRight(tc.text, " \t\r\n"))
	}
	sent = append(sent, everyConstruct)
	for _, text := range sent {
		_, err := c.CreateComment(ctx, is.ID, text)
		require.NoError(t, err, "post %q", text)
	}

	// a. through the adapter: every comment reads back exactly.
	d, err := c.FetchIssueDetail(ctx, is.ID)
	require.NoError(t, err)
	require.Len(t, d.Comments, len(sent))
	same := 0
	for i, cm := range d.Comments {
		if assert.Equal(t, sent[i], cm.Body, "comment %d", i) {
			same++
		}
	}
	t.Logf("adapter read-back: %d of %d comments identical", same, len(sent))
	assert.True(t, strings.HasPrefix(d.Comments[0].Body, questionPrefix), "the daemon still finds its question")
	assert.True(t, tracker.IsManagedComment(d.Comments[0]), "the managed marker survives")

	// b. from Jira itself: the stored bodies hold rich nodes, and Jira renders
	// them as HTML.
	user, token, _ := strings.Cut(apiKey, ":")
	req, err := http.NewRequestWithContext(ctx, "GET",
		strings.TrimRight(endpoint, "/")+"/rest/api/3/issue/"+is.Identifier+"/comment?maxResults=100&expand=renderedBody", nil)
	require.NoError(t, err)
	req.SetBasicAuth(user, token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var raw struct {
		Comments []struct {
			Body         map[string]any `json:"body"`
			RenderedBody string         `json:"renderedBody"`
		} `json:"comments"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
	require.Len(t, raw.Comments, len(sent))
	stored := map[string]bool{}
	var html strings.Builder
	for _, cm := range raw.Comments {
		for _, typ := range adfTypes(cm.Body) {
			stored[typ] = true
		}
		html.WriteString(cm.RenderedBody)
	}
	for _, want := range []string{"heading", "strong", "em", "code", "link", "codeBlock", "bulletList", "orderedList", "listItem", "blockquote", "hardBreak"} {
		assert.True(t, stored[want], "Jira stored a %s", want)
	}
	// renderedBody is Jira's older HTML rendering: bold is <b>, code is <tt>.
	for _, want := range []string{"<h1", "<h2", "<b>", "<em>", "<tt>", "<pre", "<ul", "<ol", "<blockquote>", "<a href="} {
		assert.Contains(t, html.String(), want, "Jira rendered %s", want)
	}
	question, err := json.Marshal(raw.Comments[0].Body)
	require.NoError(t, err)
	t.Logf("stored body of the question comment: %s", question)
	last, err := json.Marshal(raw.Comments[len(raw.Comments)-1].Body)
	require.NoError(t, err)
	t.Logf("stored body of the every-construct comment: %s", last)
}

// liveJSON sends one request to the Jira site, outside the adapter, and
// decodes a JSON answer into out when out is not nil. It returns the status.
func liveJSON(t *testing.T, endpoint, apiKey, method, path string, in, out any) int {
	t.Helper()
	var body *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		require.NoError(t, err)
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, strings.TrimSuffix(endpoint, "/")+path, body)
	require.NoError(t, err)
	email, token, _ := strings.Cut(apiKey, ":")
	req.SetBasicAuth(email, token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode < 300 {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
	return resp.StatusCode
}

// TestJiraLiveCommentMarkers proves on a real Jira Cloud project that
// Itervox's markers travel as comment properties. It posts a managed, keyed
// question through the adapter and checks, from Jira's own answers, that the
// stored text and the rendered HTML hold no marker and that the
// itervox.managed and itervox.commentKey properties are there. It reads the
// comment back through the adapter and checks that the body is the one
// written. Then it posts two comments as a person would, outside the
// adapter: a reply that quotes the question with the managed marker pasted
// in, and a comment with the question's key marker typed in. Both read back
// as neither managed nor keyed, and FindCommentByKey still finds only the
// question. It needs the same environment as TestJiraLiveRoundTrip, logs the
// issue key, and moves the issue to the done state at the end.
func TestJiraLiveCommentMarkers(t *testing.T) {
	endpoint := os.Getenv("JIRA_ENDPOINT")
	project := os.Getenv("JIRA_PROJECT")
	apiKey := os.Getenv("JIRA_API_KEY")
	if endpoint == "" || project == "" || apiKey == "" {
		t.Skip("set JIRA_ENDPOINT, JIRA_PROJECT and JIRA_API_KEY to run the live comment marker test")
	}
	ctx := t.Context()
	c := jira.NewClient(jira.ClientConfig{Endpoint: endpoint, APIKey: apiKey, ProjectSlug: project})
	is, err := c.CreateIssue(ctx, "", tracker.NewIssue{Title: fmt.Sprintf("itervox comment markers %d", time.Now().Unix())})
	require.NoError(t, err)
	t.Logf("test issue: %s", is.Identifier)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.UpdateIssueState(cleanupCtx, is.ID, envOr("JIRA_DONE_STATE", "Done")); err != nil {
			t.Logf("cleanup: move %s to done: %v", is.Identifier, err)
		}
	})

	// a. Itervox's question, managed and keyed, through the adapter.
	key, err := outbox.NewCommentKey()
	require.NoError(t, err)
	written := tracker.MarkManagedComment(questionPrefix + "\n\nShould I keep the old API?\n\n---\n_Reply to this comment to continue._")
	created, err := c.CreateCommentWithKey(ctx, is.ID, key, written)
	require.NoError(t, err)
	want := written + "\n\n" + tracker.CommentKeyMarker(key)
	assert.Equal(t, want, created.Body, "the comment the create returns")

	// b. Jira's own answer: no marker in the stored text or the HTML, and the
	// two properties.
	var stored struct {
		Body         map[string]any `json:"body"`
		RenderedBody string         `json:"renderedBody"`
	}
	require.Equal(t, http.StatusOK, liveJSON(t, endpoint, apiKey, "GET", "/rest/api/3/issue/"+is.Identifier+"/comment/"+created.ID+"?expand=renderedBody", nil, &stored))
	storedJSON, err := json.Marshal(stored.Body)
	require.NoError(t, err)
	t.Logf("Jira's stored body: %s", storedJSON)
	t.Logf("Jira's rendered body: %s", stored.RenderedBody)
	assert.NotContains(t, string(storedJSON), "itervox:", "no marker in the stored text")
	assert.NotContains(t, stored.RenderedBody, "itervox:", "no marker in what Jira shows")
	for _, prop := range []struct{ name, want string }{
		{"itervox.managed", `{"managed":true}`},
		{"itervox.commentKey", `{"key":"` + key + `"}`},
	} {
		var got struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		require.Equal(t, http.StatusOK, liveJSON(t, endpoint, apiKey, "GET", "/rest/api/3/comment/"+created.ID+"/properties/"+prop.name, nil, &got), prop.name)
		t.Logf("Jira's comment property %s: %s", got.Key, got.Value)
		assert.JSONEq(t, prop.want, string(got.Value), prop.name)
	}

	// c. A person's reply that quotes the question with the marker pasted in,
	// and a person's comment with the question's key marker typed in, both
	// posted outside the adapter, as the Jira editor stores them.
	para := func(text string) any {
		return map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": text}}}
	}
	var reply, typed struct {
		ID string `json:"id"`
	}
	require.Equal(t, http.StatusCreated, liveJSON(t, endpoint, apiKey, "POST", "/rest/api/3/issue/"+is.Identifier+"/comment",
		map[string]any{"body": map[string]any{"type": "doc", "version": 1, "content": []any{
			map[string]any{"type": "blockquote", "content": []any{para("Should I keep the old API?"), para(tracker.ManagedCommentMarker)}},
			para("No, drop it."),
		}}}, &reply))
	require.Equal(t, http.StatusCreated, liveJSON(t, endpoint, apiKey, "POST", "/rest/api/3/issue/"+is.Identifier+"/comment",
		map[string]any{"body": map[string]any{"type": "doc", "version": 1, "content": []any{
			para("copied from somewhere: " + tracker.CommentKeyMarker(key)),
		}}}, &typed))

	// d. Through the adapter: the question reads back as written, the person's
	// comments as neither managed nor keyed.
	d, err := c.FetchIssueDetail(ctx, is.ID)
	require.NoError(t, err)
	require.Len(t, d.Comments, 3)
	byID := map[string]domain.Comment{}
	for _, cm := range d.Comments {
		byID[cm.ID] = cm
	}
	question := byID[created.ID]
	assert.Equal(t, want, question.Body, "the question reads back as written")
	assert.True(t, tracker.IsManagedComment(question))
	assert.True(t, tracker.CommentHasKey(question.Body, key))
	for _, id := range []string{reply.ID, typed.ID} {
		cm := byID[id]
		t.Logf("a person's comment %s reads as %q", id, cm.Body)
		assert.False(t, tracker.IsManagedComment(cm), "comment %s", id)
		assert.False(t, tracker.CommentHasKey(cm.Body, key), "comment %s", id)
		assert.NotContains(t, cm.Body, "<!-- itervox:", "comment %s", id)
	}
	found, ok, err := c.FindCommentByKey(ctx, is.ID, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, created.ID, found.ID, "only the property is the key")
	t.Logf("question %s, reply %s, typed key %s on %s", created.ID, reply.ID, typed.ID, is.Identifier)
}

// editLabelsLive replaces the issue's labels through PUT
// /rest/api/3/issue/{id}. The adapter never edits labels, so the test does
// it itself.
func editLabelsLive(t *testing.T, endpoint, apiKey, issueID string, labels []string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"fields": map[string]any{"labels": labels}})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
		strings.TrimSuffix(endpoint, "/")+"/rest/api/3/issue/"+issueID, bytes.NewReader(body))
	require.NoError(t, err)
	email, token, _ := strings.Cut(apiKey, ":")
	req.SetBasicAuth(email, token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "edit labels of issue %s", issueID)
}

// TestJiraLiveIncremental proves the incremental pull of the tracker store
// (#114) on a real Jira Cloud project. It creates one labelled issue with a
// branch, then moves it between two active states several times: after
// each move, FetchIssuesUpdatedSince from the move's own update time must
// list it in its new state, and the test prints how long search took to
// show the move. The branch must come back on list reads. The project's
// least recently updated issue, unchanged for longer than the whole window,
// must not be listed. Last, the label is removed and a client scoped by
// that label must report the issue gone. It needs the same environment as
// TestJiraLiveRoundTrip and moves the issue to the done state at the end.
// It never prints the API key, the site or the account.
func TestJiraLiveIncremental(t *testing.T) {
	endpoint := os.Getenv("JIRA_ENDPOINT")
	project := os.Getenv("JIRA_PROJECT")
	apiKey := os.Getenv("JIRA_API_KEY")
	if endpoint == "" || project == "" || apiKey == "" {
		t.Skip("set JIRA_ENDPOINT, JIRA_PROJECT and JIRA_API_KEY to run the live Jira incremental test")
	}
	active := splitCSV(envOr("JIRA_ACTIVE_STATES", "To Do,In Progress"))
	require.GreaterOrEqual(t, len(active), 2, "JIRA_ACTIVE_STATES needs two states to move between")
	done := envOr("JIRA_DONE_STATE", "Done")
	samples, _ := strconv.Atoi(envOr("JIRA_LAG_SAMPLES", "6"))
	const window = 4 * time.Minute // the adapter's widening (searchLag)
	ctx := t.Context()
	stamp := time.Now().Unix()
	label := fmt.Sprintf("itervox-incremental-%d", stamp)
	c := jira.NewClient(jira.ClientConfig{Endpoint: endpoint, APIKey: apiKey, ProjectSlug: project, ActiveStates: active})
	scoped := jira.NewClient(jira.ClientConfig{Endpoint: endpoint, APIKey: apiKey, ProjectSlug: project, ActiveStates: active, Labels: []string{label}})

	// The project's least recently updated issue, before anything changes.
	all, gone, err := c.FetchIssuesUpdatedSince(ctx, time.Unix(0, 0))
	require.NoError(t, err)
	require.Empty(t, gone, "an unscoped client reports nothing gone")
	var oldest *domain.Issue
	for i := range all {
		if all[i].UpdatedAt != nil && (oldest == nil || all[i].UpdatedAt.Before(*oldest.UpdatedAt)) {
			oldest = &all[i]
		}
	}
	require.NotNil(t, oldest, "the project has issues")
	t.Logf("project lists %d issues; least recently updated: %s at %s", len(all), oldest.Identifier, oldest.UpdatedAt.UTC().Format(time.RFC3339))

	is, err := c.CreateIssue(ctx, "", tracker.NewIssue{Title: fmt.Sprintf("itervox incremental %d", stamp), Labels: []string{label}, State: active[0]})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.UpdateIssueState(cleanupCtx, is.ID, done); err != nil {
			t.Logf("cleanup: move %s to %q: %v", is.ID, done, err)
		}
	})
	branch := fmt.Sprintf("itervox/incremental-%d", stamp)
	require.NoError(t, c.SetIssueBranch(ctx, is.ID, branch))
	t.Logf("created %s with label and branch", is.Identifier)

	// listedAfter polls cl's incremental pull from since until it lists the
	// issue in state with an update time at or after since (or, with
	// wantGone, reports it gone), and returns the last result and the time
	// from wrote, when the write returned, to that result.
	listedAfter := func(cl *jira.Client, since time.Time, state string, wantGone bool, wrote time.Time) ([]domain.Issue, time.Duration) {
		t.Helper()
		start := time.Now()
		for {
			changed, gone, err := cl.FetchIssuesUpdatedSince(ctx, since)
			require.NoError(t, err)
			if wantGone && slices.Contains(gone, is.ID) {
				return changed, time.Since(wrote)
			}
			for _, got := range changed {
				if !wantGone && got.ID == is.ID && strings.EqualFold(got.State, state) && got.UpdatedAt != nil && !got.UpdatedAt.Before(since) {
					return changed, time.Since(wrote)
				}
			}
			if time.Since(start) > 2*time.Minute {
				t.Fatalf("after 2 minutes the incremental pull from %s still does not show %s (state %q, gone %v)", since.UTC().Format(time.RFC3339Nano), is.Identifier, state, wantGone)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	// updatedAt reads the issue's update time by id, which is consistent
	// right after a write (search is not).
	updatedAt := func() time.Time {
		t.Helper()
		got, err := c.FetchIssueStatesByIDs(ctx, []string{is.ID})
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.NotNil(t, got[0].UpdatedAt)
		return *got[0].UpdatedAt
	}

	var last []domain.Issue
	var since time.Time
	var worst time.Duration
	for i := range samples {
		state := active[(i+1)%2]
		require.NoError(t, c.UpdateIssueState(ctx, is.ID, state))
		wrote := time.Now()
		since = updatedAt()
		var lag time.Duration
		last, lag = listedAfter(c, since, state, false, wrote)
		worst = max(worst, lag)
		t.Logf("lag sample %d: moved to %q, listed by the incremental pull %.2fs after the move returned", i+1, state, lag.Seconds())
	}
	t.Logf("lag over %d samples: worst %.2fs (the pull looks back %s plus the store's overlap)", samples, worst.Seconds(), window)

	var listed *domain.Issue
	for i := range last {
		if last[i].UpdatedAt != nil {
			assert.False(t, last[i].UpdatedAt.Before(since.Add(-window)), "%s is older than the window", last[i].Identifier)
		}
		if last[i].ID == is.ID {
			listed = &last[i]
		}
		assert.NotEqual(t, oldest.ID, last[i].ID, "the unchanged older issue is not listed")
	}
	require.NotNil(t, listed)
	if assert.NotNil(t, listed.BranchName, "the incremental pull carries the branch") {
		assert.Equal(t, branch, *listed.BranchName)
	}
	t.Logf("incremental pull lists %d issues; %s not among them (updated %s, before the window start %s)",
		len(last), oldest.Identifier, oldest.UpdatedAt.UTC().Format(time.RFC3339), since.Add(-window).UTC().Format(time.RFC3339))
	cands, err := c.FetchCandidateIssues(ctx)
	require.NoError(t, err)
	var inCands bool
	for _, got := range cands {
		if got.ID == is.ID {
			inCands = true
			if assert.NotNil(t, got.BranchName, "the candidate poll carries the branch") {
				assert.Equal(t, branch, *got.BranchName)
			}
		}
	}
	assert.True(t, inCands, "the issue is a candidate")

	// The label is removed: the label-scoped client reports the issue gone.
	before := updatedAt()
	editLabelsLive(t, endpoint, apiKey, is.ID, []string{})
	wrote := time.Now()
	require.True(t, updatedAt().After(before), "removing a label moves the update time")
	_, lag := listedAfter(scoped, before, "", true, wrote)
	t.Logf("label removed: the label-scoped pull reports %s gone %.2fs after the edit returned", is.Identifier, lag.Seconds())
}

// TestJiraLiveStore runs the tracker store's sync loop (store.Run, as the
// daemon starts it) over the adapter on a real Jira Cloud project, with a
// 2-second sync interval, an hourly full pull and the store settings the
// daemon uses for Jira. It creates an issue in an
// active state and waits until the store lists it as a candidate; then it
// moves the issue to the done state through the adapter, as a person would
// in Jira, and waits until the store's candidates drop it and its board
// view shows it done, which only the incremental pulls can do within the
// hour. It prints how long each took. It needs the same environment as
// TestJiraLiveRoundTrip. It never prints the API key, the site or the
// account.
func TestJiraLiveStore(t *testing.T) {
	endpoint := os.Getenv("JIRA_ENDPOINT")
	project := os.Getenv("JIRA_PROJECT")
	apiKey := os.Getenv("JIRA_API_KEY")
	if endpoint == "" || project == "" || apiKey == "" {
		t.Skip("set JIRA_ENDPOINT, JIRA_PROJECT and JIRA_API_KEY to run the live Jira store test")
	}
	active := splitCSV(envOr("JIRA_ACTIVE_STATES", "To Do,In Progress"))
	done := envOr("JIRA_DONE_STATE", "Done")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := jira.NewClient(jira.ClientConfig{Endpoint: endpoint, APIKey: apiKey, ProjectSlug: project, ActiveStates: active})
	boardStates := append(slices.Clone(active), done)
	tr, st, err := store.Wrap(c, store.Config{
		Path:             t.TempDir() + "/tracker_store.json",
		Scope:            "jira-live",
		ActiveStates:     active,
		Views:            [][]string{boardStates},
		SyncInterval:     2 * time.Second,
		FullSyncInterval: time.Hour,
		// As withTrackerStore sets it for Jira: the cold pull's candidate
		// search can miss the issue just created while its view search
		// lists it.
		ListingsLagWrites: true,
	})
	require.NoError(t, err)
	_, ok := tr.(tracker.ScopeMatcher)
	require.True(t, ok)

	is, err := c.CreateIssue(ctx, "", tracker.NewIssue{Title: fmt.Sprintf("itervox store %d", time.Now().Unix()), State: active[0]})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := c.UpdateIssueState(cleanupCtx, is.ID, done); err != nil {
			t.Logf("cleanup: move %s to %q: %v", is.ID, done, err)
		}
	})
	created := time.Now()
	runDone := make(chan struct{})
	go func() { defer close(runDone); st.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-runDone })

	// waitFor polls the store (no request: it answers from its copy) until
	// cond holds.
	waitFor := func(what string, cond func() bool) time.Duration {
		t.Helper()
		start := time.Now()
		for !cond() {
			if time.Since(start) > 2*time.Minute {
				t.Fatalf("after 2 minutes: %s", what)
			}
			time.Sleep(200 * time.Millisecond)
		}
		return time.Since(start)
	}
	stateIn := func(list []domain.Issue) (string, bool) {
		for _, got := range list {
			if got.ID == is.ID {
				return got.State, true
			}
		}
		return "", false
	}
	waitFor("the store lists the new issue as a candidate", func() bool {
		cands, err := tr.FetchCandidateIssues(ctx)
		require.NoError(t, err)
		_, in := stateIn(cands)
		return in
	})
	t.Logf("%s: a candidate in the store %.2fs after it was created", is.Identifier, time.Since(created).Seconds())
	cursor := st.SyncStatus().Cursor

	require.NoError(t, c.UpdateIssueState(ctx, is.ID, done))
	moved := time.Now()
	waitFor("the store drops the done issue from the candidates and shows it done", func() bool {
		cands, err := tr.FetchCandidateIssues(ctx)
		require.NoError(t, err)
		if _, in := stateIn(cands); in {
			return false
		}
		board, err := tr.FetchIssuesByStates(ctx, boardStates)
		require.NoError(t, err)
		state, in := stateIn(board)
		return in && strings.EqualFold(state, done)
	})
	t.Logf("%s: moved to %q in Jira; the store shows it %.2fs later (sync interval 2s, full pull hourly)", is.Identifier, done, time.Since(moved).Seconds())
	status := st.SyncStatus()
	assert.Zero(t, status.Failures)
	assert.True(t, status.Cursor.After(cursor), "the cursor moved to the move's update time")
}
