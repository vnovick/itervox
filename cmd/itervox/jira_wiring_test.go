package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// TestBuildTrackerJiraPassesTheScope: tracker.assignee and tracker.labels
// reach the Jira client, so its poll is scoped.
func TestBuildTrackerJiraPassesTheScope(t *testing.T) {
	jqls := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JQL string `json:"jql"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		jqls <- body.JQL
		_, _ = w.Write([]byte(`{"issues":[],"isLast":true}`))
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.Tracker.Kind = "jira"
	cfg.Tracker.Endpoint = srv.URL
	cfg.Tracker.APIKey = "me@example.com:tok"
	cfg.Tracker.ProjectSlug = "PROJ"
	cfg.Tracker.ActiveStates = []string{"To Do"}
	cfg.Tracker.Assignee = "acc-ada"
	cfg.Tracker.Labels = []string{"agent"}

	tr, err := buildTracker(cfg)
	require.NoError(t, err)
	_, err = tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)

	assert.Equal(t, `project = "PROJ" AND assignee = "acc-ada" AND labels in ("agent") AND status in ("To Do") ORDER BY created ASC`, <-jqls)
	_, ok := tr.(tracker.ScopeMatcher)
	assert.True(t, ok, "the jira client checks its scope on a retry's by-ID read")
}

// TestDoctorChecksJiraAssignee: `itervox doctor` resolves a Jira
// tracker.assignee and counts the issues the scoped poll reads; an account
// id that no account has fails the check (exit 1).
func TestDoctorChecksJiraAssignee(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/3/myself":
			_, _ = w.Write([]byte(`{"accountId":"acc-me","displayName":"Itervox Bot"}`))
		case "/rest/api/3/user":
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = w.Write([]byte(`{"issues":[{"id":"1","key":"PROJ-1","fields":{}},{"id":"2","key":"PROJ-2","fields":{}}],"isLast":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	workflow := func(scope string) string {
		path := filepath.Join(t.TempDir(), "WORKFLOW.md")
		body := "---\nitervox_schema_version: 2\ntracker:\n  kind: jira\n  endpoint: " + srv.URL + "\n  project_slug: PROJ\n" +
			"  api_key: me@example.com:tok\n  active_states: [\"To Do\"]\n" + scope + "server:\n  port: 0\n---\n\nPrompt.\n"
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}

	report, _ := collectDoctorReport(workflow("  assignee: me\n  labels: [agent]\n"))
	require.True(t, report.Assignee.Ran)
	assert.False(t, assigneeCheckFailed(report.Assignee))
	out := renderDoctorReport(report)
	assert.Contains(t, out, `assignee scope: "me" resolves to Itervox Bot`)
	assert.Contains(t, out, "assignee reads: OK — 2 issue(s) in the active states of project PROJ match tracker.assignee and tracker.labels")

	report, _ = collectDoctorReport(workflow("  assignee: acc-nobody\n"))
	assert.True(t, report.Assignee.Unresolvable)
	assert.True(t, assigneeCheckFailed(report.Assignee))
	assert.Equal(t, 1, doctorExitCode(report))
	assert.Contains(t, renderDoctorReport(report), `ERROR: tracker.assignee "acc-nobody" does not resolve to an account this API token can see — use me or an Atlassian account id`)

	report, _ = collectDoctorReport(workflow("  labels: [agent]\n"))
	assert.False(t, report.Assignee.Ran, "no assignee, no check")
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
