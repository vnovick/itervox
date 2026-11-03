package hub

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryRouteDeclaresAScope (#214, #228) for the hub's own router: every
// registered route has a hubRouteScopes line (a Handle route under "*"),
// every line names a registered route, and every non-public route answers
// 401 without a hub token. internal/server's test of the same name covers
// the daemon routes, GET /api/v1/route-scopes included.
func TestEveryRouteDeclaresAScope(t *testing.T) {
	h, err := newHub(Config{StateDir: filepath.Join(t.TempDir(), "hub"), RegistryDir: t.TempDir()})
	require.NoError(t, err)
	used := map[string]bool{}
	var undeclared []string
	err = chi.Walk(h.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		switch {
		case hubRouteScopes[method+" "+route] != "":
			used[method+" "+route] = true
		case hubRouteScopes["* "+route] != "":
			used["* "+route] = true
		default:
			undeclared = append(undeclared, method+" "+route)
		}
		return nil
	})
	require.NoError(t, err)
	var stale []string
	for key := range hubRouteScopes {
		if !used[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(undeclared)
	sort.Strings(stale)
	assert.Empty(t, undeclared, "hub routes registered without a hubRouteScopes line")
	assert.Empty(t, stale, "hubRouteScopes lines with no registered route")

	for key, access := range hubRouteScopes {
		if access == hubPublic {
			continue
		}
		method, pattern, _ := strings.Cut(key, " ")
		if method == "*" {
			method = http.MethodGet
		}
		path := strings.ReplaceAll(strings.ReplaceAll(pattern, "{project}", "x"), "*", "v1/state")
		rec := httptestDo(h.Handler(), method, path)
		assert.Equal(t, http.StatusUnauthorized, rec, "%s without a hub token", key)
	}
}

func httptestDo(h http.Handler, method, path string) int {
	req, _ := http.NewRequest(method, path, nil)
	rw := &statusRecorder{header: http.Header{}}
	h.ServeHTTP(rw, req)
	return rw.status
}

type statusRecorder struct {
	header http.Header
	status int
}

func (s *statusRecorder) Header() http.Header { return s.header }

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return len(b), nil
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code }

// TestRouteTableMatchIsMostRestrictive: a path several patterns match takes
// the most restrictive access, unknown classes refuse, and {param} matches
// one non-empty segment only.
func TestRouteTableMatchIsMostRestrictive(t *testing.T) {
	tbl, err := parseRouteTable([]byte(`{"routes":{
		"GET /api/v1/memory/recall":"viewer",
		"GET /api/v1/memory/{id}":"operator",
		"POST /api/v1/agent-actions/{identifier}/comment":"agent_grant",
		"GET /api/v1/odd":"mystery",
		"* /*":"public"}}`))
	require.NoError(t, err)
	access, ok := tbl.match(http.MethodGet, "/api/v1/memory/recall")
	assert.True(t, ok)
	assert.Equal(t, "operator", access)
	access, _ = tbl.match(http.MethodGet, "/api/v1/odd")
	assert.Equal(t, 4, accessRank(access))
	_, ok = tbl.match(http.MethodGet, "/api/v1/memory/")
	assert.False(t, ok)
	_, ok = tbl.match(http.MethodGet, "/api/v1/memory/a/b")
	assert.False(t, ok)
	_, ok = tbl.match(http.MethodDelete, "/api/v1/memory/a")
	assert.False(t, ok)
}

// TestProjectNamesAreUniqueAndStable: duplicates get a suffix derived from
// the workflow path only.
func TestProjectNamesAreUniqueAndStable(t *testing.T) {
	recs := []MemberRecord{
		{Workflow: "/a/web/WORKFLOW.md", ProjectDir: "/a/web"},
		{Workflow: "/b/Web/WORKFLOW.md", ProjectDir: "/b/Web"},
		{Workflow: "/c/api/WORKFLOW.md", ProjectDir: "/c/api"},
		{Workflow: "/d/.hidden dir/WORKFLOW.md", ProjectDir: "/d/.hidden dir"},
	}
	names := ProjectNames(recs)
	assert.Equal(t, "api", names["/c/api/WORKFLOW.md"])
	assert.Equal(t, "hidden-dir", names["/d/.hidden dir/WORKFLOW.md"])
	a, b := names["/a/web/WORKFLOW.md"], names["/b/Web/WORKFLOW.md"]
	assert.NotEqual(t, a, b)
	assert.True(t, strings.HasPrefix(a, "web-") && strings.HasPrefix(b, "web-"))
	again := ProjectNames(recs[:2])
	assert.Equal(t, a, again["/a/web/WORKFLOW.md"], "a name depends only on the path")
}
