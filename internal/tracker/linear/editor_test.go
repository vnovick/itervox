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

// gqlFake answers the sync's Linear operations by name and records every
// request.
type gqlFake struct {
	mu   sync.Mutex
	reqs []gqlRequest
}

func (f *gqlFake) named(name string) []gqlRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gqlRequest
	for _, r := range f.reqs {
		if strings.Contains(r.Query, name) {
			out = append(out, r)
		}
	}
	return out
}

func newGQLFake(t *testing.T) (*gqlFake, *linear.Client) {
	f := &gqlFake{}
	labels := map[string]any{"nodes": []any{
		map[string]any{"id": "lbl-cli", "name": "CLI"}, map[string]any{"id": "lbl-bug", "name": "bug"}}}
	ws := map[string]any{"nodes": []any{map[string]any{"id": "lbl-ws", "name": "urgent"}}}
	issueNode := map[string]any{"id": "uuid-new", "identifier": "ENG-9", "title": "T", "state": map[string]any{"name": "Todo"},
		"priority": float64(2), "updatedAt": "2026-10-14T10:00:00.000Z"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gqlRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		var data map[string]any
		switch {
		case strings.Contains(req.Query, "ItervoxSyncProjectContext"):
			data = map[string]any{"projects": map[string]any{"nodes": []any{map[string]any{"id": "proj-1",
				"teams": map[string]any{"nodes": []any{map[string]any{"id": "team-1",
					"states": map[string]any{"nodes": []any{map[string]any{"id": "st-todo", "name": "Todo"}}},
					"labels": labels}}}}}}, "issueLabels": ws}
		case strings.Contains(req.Query, "ItervoxSyncIssueContext"):
			data = map[string]any{"issue": map[string]any{"id": "uuid-7", "team": map[string]any{"id": "team-1", "labels": labels},
				"inverseRelations": map[string]any{"nodes": []any{
					map[string]any{"id": "rel-a", "type": "blocks", "issue": map[string]any{"id": "uuid-a"}},
					map[string]any{"id": "rel-dup", "type": "duplicate", "issue": map[string]any{"id": "uuid-x"}}}}},
				"issueLabels": ws}
		case strings.Contains(req.Query, "ItervoxSyncIssueCreate"):
			data = map[string]any{"issueCreate": map[string]any{"success": true, "issue": issueNode}}
		case strings.Contains(req.Query, "ItervoxSyncIssueUpdate"):
			data = map[string]any{"issueUpdate": map[string]any{"success": true}}
		case strings.Contains(req.Query, "ItervoxSyncRelationCreate"):
			data = map[string]any{"issueRelationCreate": map[string]any{"success": true}}
		case strings.Contains(req.Query, "ItervoxSyncRelationDelete"):
			data = map[string]any{"issueRelationDelete": map[string]any{"success": true}}
		case strings.Contains(req.Query, "ItervoxSyncIssueByKey"):
			nodes := []any{}
			if req.Variables["key"] == "uuid-new" {
				nodes = append(nodes, issueNode)
			}
			data = map[string]any{"issues": map[string]any{"nodes": nodes}}
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	t.Cleanup(srv.Close)
	return f, linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL, ProjectSlug: "eng-board"})
}

// TestLinearCreateScopedIssue (#118): the issue is created in project_slug's
// project and team, under the client key as its id, with its state,
// priority and labels resolved to ids (ignoring case), and its blockers as
// "blocks" relations; FindIssueByKey finds it by that key.
func TestLinearCreateScopedIssue(t *testing.T) {
	f, client := newGQLFake(t)
	p := 2
	is, err := client.CreateScopedIssue(t.Context(), tracker.IssueDraft{Key: "uuid-new", Title: "T", Description: "D",
		State: "todo", Labels: []string{"cli", "Urgent", "missing"}, Priority: &p, BlockedBy: []string{"uuid-a"}})
	require.NoError(t, err)
	assert.Equal(t, "ENG-9", is.Identifier)

	create := f.named("ItervoxSyncIssueCreate")
	require.Len(t, create, 1)
	input := create[0].Variables["input"].(map[string]any)
	assert.Equal(t, "uuid-new", input["id"])
	assert.Equal(t, "team-1", input["teamId"])
	assert.Equal(t, "proj-1", input["projectId"])
	assert.Equal(t, "st-todo", input["stateId"])
	assert.InDelta(t, 2, input["priority"], 0)
	assert.Equal(t, []any{"lbl-cli", "lbl-ws"}, input["labelIds"], "unknown labels are left out")
	rel := f.named("ItervoxSyncRelationCreate")
	require.Len(t, rel, 1)
	assert.Equal(t, map[string]any{"issueId": "uuid-a", "relatedIssueId": "uuid-new", "type": "blocks"}, rel[0].Variables["input"])

	got, found, err := client.FindIssueByKey(t.Context(), "uuid-new")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "ENG-9", got.Identifier)
	_, found, err = client.FindIssueByKey(t.Context(), "uuid-other")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestLinearUpdateIssueFields (#118): one issueUpdate carries title,
// description, priority (-1 clears it to 0) and label ids; blockers become
// exactly the given "blocks" relations, other relation types untouched.
func TestLinearUpdateIssueFields(t *testing.T) {
	f, client := newGQLFake(t)
	title, desc, prio := "New", "Body", -1
	labels := []string{"bug"}
	blockers := []string{"uuid-b"}
	require.NoError(t, client.UpdateIssueFields(t.Context(), "uuid-7", tracker.IssueFieldsPatch{
		Title: &title, Description: &desc, Priority: &prio, Labels: &labels, BlockedBy: &blockers}))

	upd := f.named("ItervoxSyncIssueUpdate")
	require.Len(t, upd, 1)
	assert.Equal(t, "uuid-7", upd[0].Variables["id"])
	assert.Equal(t, map[string]any{"title": "New", "description": "Body", "priority": float64(0), "labelIds": []any{"lbl-bug"}},
		upd[0].Variables["input"])
	created := f.named("ItervoxSyncRelationCreate")
	require.Len(t, created, 1)
	assert.Equal(t, "uuid-b", created[0].Variables["input"].(map[string]any)["issueId"])
	deleted := f.named("ItervoxSyncRelationDelete")
	require.Len(t, deleted, 1)
	assert.Equal(t, "rel-a", deleted[0].Variables["id"], "only the stale blocks relation is deleted")

	// A title-only patch is one request, no context read.
	before := len(f.named("Itervox"))
	require.NoError(t, client.UpdateIssueFields(t.Context(), "uuid-7", tracker.IssueFieldsPatch{Title: &title}))
	assert.Len(t, f.named("Itervox"), before+1)
}
