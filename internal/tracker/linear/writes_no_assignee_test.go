package linear_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/linear"
)

// TestAgentWritesNeverSetTheAssignee (#182): every write the daemon makes on
// an agent's behalf — state moves, comments, the branch name, follow-up
// issues, field edits — sends no assignee, so a run can never move its own
// issue out of an assignee scope. Reassignment is a human action. The check
// covers each mutation's document and its variables.
func TestAgentWritesNeverSetTheAssignee(t *testing.T) {
	var mu sync.Mutex
	var mutations []gqlRequest
	issueNode := map[string]any{"id": issueUUID(9), "identifier": "ENG-9", "title": "T", "state": map[string]any{"name": "Todo"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gqlRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		op := opNameRe.FindStringSubmatch(req.Query)[1]
		if strings.HasPrefix(strings.TrimSpace(req.Query), "mutation") {
			mu.Lock()
			mutations = append(mutations, req)
			mu.Unlock()
		}
		var data map[string]any
		switch op {
		case "ItervoxResolveStateId", "ItervoxResolveCreateIssueContext":
			data = map[string]any{"issue": map[string]any{
				"team":    map[string]any{"id": "team-1", "states": map[string]any{"nodes": []any{map[string]any{"id": "state-1", "name": "Done"}}}},
				"project": map[string]any{"id": "project-1"},
			}}
		case "ItervoxUpdateIssueState", "ItervoxSetBranchName", "ItervoxSyncIssueUpdate":
			data = map[string]any{"issueUpdate": map[string]any{"success": true}}
		case "ItervoxCreateComment", "ItervoxCreateCommentWithKey":
			data = map[string]any{"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": "c-1", "body": "b"}}}
		case "ItervoxCreateIssue":
			data = map[string]any{"issueCreate": map[string]any{"success": true, "issue": issueNode}}
		default:
			t.Errorf("unexpected operation %s", op)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	c := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL, Assignee: "me"})
	ctx := t.Context()
	id := issueUUID(9)

	require.NoError(t, c.UpdateIssueState(ctx, id, "Done"))
	_, err := c.CreateComment(ctx, id, "progress")
	require.NoError(t, err)
	_, err = c.CreateCommentWithKey(ctx, id, "99999999-9999-4999-8999-999999999999", "question")
	require.NoError(t, err)
	require.NoError(t, c.SetIssueBranch(ctx, id, "eng-9"))
	_, err = c.CreateIssue(ctx, id, "follow-up", "body", "Done")
	require.NoError(t, err)
	title := "renamed"
	require.NoError(t, c.UpdateIssueFields(ctx, id, tracker.IssueFieldsPatch{Title: &title}))

	require.Len(t, mutations, 6, "one mutation per write")
	for _, m := range mutations {
		assert.NotContains(t, strings.ToLower(m.Query), "assignee", "mutation document: %s", m.Query)
		raw, err := json.Marshal(m.Variables)
		require.NoError(t, err)
		assert.NotContains(t, strings.ToLower(string(raw)), "assignee", "mutation variables: %s", raw)
	}
}
