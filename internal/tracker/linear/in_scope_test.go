package linear_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker/linear"
)

func scopedNode(id, identifier string, projectSlug string) map[string]interface{} {
	n := map[string]interface{}{
		"id": id, "identifier": identifier, "title": "T " + identifier,
		"state": map[string]interface{}{"name": "Todo"},
	}
	if projectSlug != "" {
		n["project"] = map[string]interface{}{"slugId": projectSlug}
	} else {
		n["project"] = nil
	}
	return n
}

// TestFetchIssuesInScopeHonoursTheProjectFilter (#131): issues read by ID are
// kept only when the project filter covers them, with the same rules as the
// candidate poll — and in one request.
func TestFetchIssuesInScopeHonoursTheProjectFilter(t *testing.T) {
	resp := map[string]interface{}{"data": map[string]interface{}{"issues": map[string]interface{}{
		"nodes": []interface{}{
			scopedNode("id-1", "ENG-1", "ours"),
			scopedNode("id-2", "ENG-2", "theirs"),
			scopedNode("id-3", "ENG-3", ""),
		},
	}}}
	ids := []string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000003"}
	identifiers := func(t *testing.T, client *linear.Client) []string {
		t.Helper()
		got, err := client.FetchIssuesInScope(context.Background(), ids)
		require.NoError(t, err)
		var out []string
		for _, iss := range got {
			out = append(out, iss.Identifier)
		}
		return out
	}

	srv := serveJSON(t, []map[string]interface{}{resp, resp, resp, resp})
	defer srv.Close()
	client := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL, ProjectSlug: "ours"})
	assert.Equal(t, []string{"ENG-1"}, identifiers(t, client), "project_slug is the scope")

	client.SetProjectFilter([]string{"theirs", "__no_project__"})
	assert.Equal(t, []string{"ENG-2", "ENG-3"}, identifiers(t, client), "the runtime filter, with the no-project bucket")

	client.SetProjectFilter([]string{})
	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, identifiers(t, client), "an empty filter is every project")

	noSlug := linear.NewClient(linear.ClientConfig{APIKey: "k", Endpoint: srv.URL})
	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, identifiers(t, noSlug), "no project_slug is every project")
}
