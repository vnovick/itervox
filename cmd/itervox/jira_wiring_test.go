package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

func TestBuildTrackerJira(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.Kind = "jira"
	cfg.Tracker.Endpoint = "https://example.atlassian.net"
	cfg.Tracker.APIKey = "me@example.com:tok"
	cfg.Tracker.ProjectSlug = "PROJ"
	cfg.Tracker.ActiveStates = []string{"To Do"}
	cfg.Tracker.TerminalStates = []string{"Done"}

	tr, err := buildTracker(cfg)
	require.NoError(t, err)
	_, ok := tr.(*jira.Client)
	require.True(t, ok, "kind jira builds the jira client, got %T", tr)
	_, ok = tr.(tracker.StateListSetter)
	assert.True(t, ok, "the jira client implements StateListSetter")
}

func TestDoctorDeployJiraProbe(t *testing.T) {
	rows := []struct {
		name       string
		myself     int
		wantStatus deployStatus
		wantDetail string
	}{
		{"ok", http.StatusOK, deployOK, "Ada Lovelace"},
		{"bad token", http.StatusUnauthorized, deployFail, "jira probe failed: "},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rest/api/3/myself" {
					w.WriteHeader(row.myself)
					_, _ = w.Write([]byte(`{"accountId":"abc","displayName":"Ada Lovelace"}`))
					return
				}
				_, _ = w.Write([]byte(`{"key":"PROJ"}`))
			}))
			t.Cleanup(srv.Close)
			cfg := &config.Config{}
			cfg.Tracker.Kind = "jira"
			cfg.Tracker.Endpoint = srv.URL
			cfg.Tracker.APIKey = "me@example.com:tok"
			cfg.Tracker.ProjectSlug = "PROJ"

			got := trackerAPICheck(t.Context(), cfg, defaultDeployProbeEnv())
			assert.Equal(t, row.wantStatus, got.Status, got.Detail)
			assert.Contains(t, got.Detail, row.wantDetail)
		})
	}

	t.Run("empty key", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Tracker.Kind = "jira"
		got := trackerAPICheck(t.Context(), cfg, defaultDeployProbeEnv())
		assert.Equal(t, deployFail, got.Status)
		assert.Contains(t, got.Detail, "JIRA_API_KEY")
	})
}
