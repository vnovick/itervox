package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// modelServersServer is a token-mode server whose client lists rows, with a
// stored viewer token.
func modelServersServer(t *testing.T, client OrchestratorClient) (*Server, string) {
	t.Helper()
	store := apitokens.Open(filepath.Join(t.TempDir(), ".itervox", apitokens.FileName))
	_, viewer, err := store.Create("viewer-token", apitokens.ScopeViewer, nil, "test")
	require.NoError(t, err)
	srv := New(Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		Client:      client,
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
	})
	return srv, viewer
}

// TestHandleModelServers: GET /api/v1/model-servers lists the client's rows
// in the documented shape, viewer scope; pools and profiles are always
// arrays, unchecked appears only when set, and URL credentials are
// redacted below admin.
func TestHandleModelServers(t *testing.T) {
	rows := []ModelServerRow{
		{Var: "ANTHROPIC_BASE_URL", URL: "http://127.0.0.1:8000", Profiles: []string{"(default)", "coder-local"}, Pools: []string{"local"}},
		{Var: "OPENAI_BASE_URL", URL: "$LOCAL_URL", Profiles: []string{"from-var"}, Unchecked: true},
		{Var: "OPENAI_API_BASE", URL: "http://user:hunter2@localhost:9000/v1", Profiles: []string{"proxy"}, Remote: true},
	}
	srv, viewer := modelServersServer(t, &FuncClient{ModelServersFn: func() []ModelServerRow { return rows }})

	rec := doReq(t, srv, http.MethodGet, "/api/v1/model-servers", viewer)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got struct {
		Servers []map[string]any `json:"servers"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Servers, 3)
	assert.Equal(t, map[string]any{
		"var": "ANTHROPIC_BASE_URL", "url": "http://127.0.0.1:8000",
		"profiles": []any{"(default)", "coder-local"}, "pools": []any{"local"}, "remote": false,
	}, got.Servers[0])
	assert.Equal(t, map[string]any{
		"var": "OPENAI_BASE_URL", "url": "$LOCAL_URL",
		"profiles": []any{"from-var"}, "pools": []any{}, "remote": false, "unchecked": true,
	}, got.Servers[1])
	assert.Equal(t, true, got.Servers[2]["remote"])
	assert.NotContains(t, rec.Body.String(), "hunter2", "viewer: URL credentials redacted")
	assert.Contains(t, got.Servers[2]["url"], "@localhost:9000/v1")

	admin := doReq(t, srv, http.MethodGet, "/api/v1/model-servers", scopesTestBootstrap)
	require.Equal(t, http.StatusOK, admin.Code)
	assert.Contains(t, admin.Body.String(), "hunter2", "admin sees the URL as written")
	assert.Equal(t, "http://user:hunter2@localhost:9000/v1", rows[2].URL, "redaction never mutates the client's rows")

	assert.Equal(t, http.StatusUnauthorized, doReq(t, srv, http.MethodGet, "/api/v1/model-servers", "").Code)
}

// TestHandleModelServersWithoutLister: a client without the capability
// (and a lister with nothing configured) answers an empty list, not null.
func TestHandleModelServersWithoutLister(t *testing.T) {
	for _, client := range []OrchestratorClient{nil, &FuncClient{}} {
		srv, viewer := modelServersServer(t, client)
		rec := doReq(t, srv, http.MethodGet, "/api/v1/model-servers", viewer)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"servers":[]}`, rec.Body.String())
	}
}
