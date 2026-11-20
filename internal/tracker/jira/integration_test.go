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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
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
	assert.LessOrEqual(t, units(stored), jiraLimit)
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
