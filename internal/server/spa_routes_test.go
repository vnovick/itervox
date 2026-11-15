package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The issue view (/focus, /focus/{identifier}) is a client-side route: the
// daemon serves the dashboard's index.html for it, uncached, like every
// other page the app routes itself.
func TestDashboardServesIndexForTheIssueViewRoutes(t *testing.T) {
	index := httptest.NewRecorder()
	DashboardHandler().ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, index.Code)
	want := index.Body.String()
	require.True(t, strings.Contains(strings.ToLower(want), "<html"), "the embedded dashboard has an index.html")

	for _, path := range []string{"/focus", "/focus/WEB-207", "/focus/ENG-1%2F2"} {
		rec := httptest.NewRecorder()
		DashboardHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, want, rec.Body.String(), "%s serves index.html", path)
		assert.Equal(t, "no-cache, no-store, must-revalidate", rec.Header().Get("Cache-Control"), path)
	}
}
