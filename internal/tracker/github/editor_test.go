package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker"
	ghclient "github.com/vnovick/itervox/internal/tracker/github"
)

// editServer records the PATCH and POST bodies and serves one issue.
type editServer struct {
	mu      sync.Mutex
	issue   map[string]any
	patches []map[string]any
	posts   []map[string]any
}

func newEditServer(t *testing.T, issue map[string]any) (*editServer, *httptest.Server) {
	s := &editServer{issue: issue}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var body map[string]any
		if r.Body != nil && r.Method != http.MethodGet {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/7":
			_ = json.NewEncoder(w).Encode(s.issue)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/owner/repo/issues/7":
			s.patches = append(s.patches, body)
			_ = json.NewEncoder(w).Encode(s.issue)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues":
			s.posts = append(s.posts, body)
			created := ghIssue(8, body["title"].(string), "open", []string{"todo"})
			created["body"] = body["body"]
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(created)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

// TestGHUpdateIssueFieldsKeepsStateAndPriorityLabels (#118): a labels patch
// replaces the issue's own labels and keeps its state label and pN label; a
// priority patch swaps the pN label; a blockers patch rewrites one managed
// body line that reads back as the issue's blockers, and a description
// patch keeps that line.
func TestGHUpdateIssueFieldsKeepsStateAndPriorityLabels(t *testing.T) {
	issue := ghIssue(7, "Old", "open", []string{"todo", "bug", "p2"})
	issue["body"] = "The description.\n\nBlocked by #3 <!-- itervox:blocked-by -->"
	s, srv := newEditServer(t, issue)
	client := ghclient.NewClient(defaultConfig(srv.URL))

	title, labels := "New", []string{"cli"}
	require.NoError(t, client.UpdateIssueFields(t.Context(), "7", tracker.IssueFieldsPatch{Title: &title, Labels: &labels}))
	require.Len(t, s.patches, 1)
	assert.Equal(t, "New", s.patches[0]["title"])
	assert.ElementsMatch(t, []any{"todo", "p2", "cli"}, s.patches[0]["labels"])
	assert.NotContains(t, s.patches[0], "body", "no body change asked for")

	p := 0
	require.NoError(t, client.UpdateIssueFields(t.Context(), "7", tracker.IssueFieldsPatch{Priority: &p}))
	assert.ElementsMatch(t, []any{"todo", "bug", "p0"}, s.patches[1]["labels"])

	desc := "A new description."
	require.NoError(t, client.UpdateIssueFields(t.Context(), "7", tracker.IssueFieldsPatch{Description: &desc}))
	assert.Equal(t, "A new description.\n\nBlocked by #3 <!-- itervox:blocked-by -->", s.patches[2]["body"])

	blockers := []string{"4", "5"}
	require.NoError(t, client.UpdateIssueFields(t.Context(), "7", tracker.IssueFieldsPatch{BlockedBy: &blockers}))
	body := s.patches[3]["body"].(string)
	assert.Equal(t, "The description.\n\nBlocked by #4, #5 <!-- itervox:blocked-by -->", body)

	assert.Equal(t, []string{"4", "5"}, ghclient.ExtractBlockerNumbers(body), "the managed line reads back as the blockers")

	none := []string{}
	require.NoError(t, client.UpdateIssueFields(t.Context(), "7", tracker.IssueFieldsPatch{BlockedBy: &none}))
	assert.Equal(t, "The description.", s.patches[len(s.patches)-1]["body"])
}

// TestGHCreateScopedIssue (#118): the new issue carries its state label,
// labels, pN label and blockers line.
func TestGHCreateScopedIssue(t *testing.T) {
	s, srv := newEditServer(t, nil)
	client := ghclient.NewClient(defaultConfig(srv.URL))
	p := 1
	is, err := client.CreateScopedIssue(t.Context(), tracker.IssueDraft{Key: "k", Title: "T", Description: "D", State: "todo",
		Labels: []string{"cli"}, Priority: &p, BlockedBy: []string{"3"}})
	require.NoError(t, err)
	assert.Equal(t, "#8", is.Identifier)
	require.Len(t, s.posts, 1)
	assert.Equal(t, []any{"todo", "cli", "p1"}, s.posts[0]["labels"])
	assert.Equal(t, "D\n\nBlocked by #3 <!-- itervox:blocked-by -->", s.posts[0]["body"])
	_, found, err := client.FindIssueByKey(t.Context(), "k")
	require.NoError(t, err)
	assert.False(t, found, "GitHub cannot look a create key up")
}
