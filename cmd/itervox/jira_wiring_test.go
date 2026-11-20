package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/jira"
)

// scaffoldJiraProject writes the project `itervox init --tracker jira`
// writes, in a temp directory, with model discovery stubbed out.
func scaffoldJiraProject(t *testing.T) string {
	t.Helper()
	t.Setenv(config.UserSettingsEnv, "off")
	origClaude, origCodex := listClaudeModels, listCodexModels
	listClaudeModels = func() []agent.ModelOption { return nil }
	listCodexModels = func() []agent.ModelOption { return nil }
	t.Cleanup(func() { listClaudeModels, listCodexModels = origClaude, origCodex })
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, scaffoldWorkflow(path, dir, "jira", "claude", initOptions{}, io.Discard))
	return path
}

// fillJiraSetting replaces the empty value init writes for key in the
// project settings file, as an operator does by hand.
func fillJiraSetting(t *testing.T, path, key, value string) {
	t.Helper()
	settingsPath := config.ProjectSettingsPath(path)
	re := regexp.MustCompile(`("` + key + `":\s*)""`)
	text := readString(t, settingsPath)
	require.Len(t, re.FindAllString(text, -1), 1, "init writes one empty %s", key)
	require.NoError(t, os.WriteFile(settingsPath, []byte(re.ReplaceAllString(text, `${1}"`+value+`"`)), 0o644))
}

// TestInitJiraScaffold: `itervox init --tracker jira` puts the tracker in
// .itervox/settings.jsonc with the key as a $VAR reference and states that
// every new Jira software project has. The site URL and the project key are
// left empty, so the workflow does not validate (and sends nothing anywhere)
// until the operator fills both in; then it validates and builds the Jira
// client.
func TestInitJiraScaffold(t *testing.T) {
	path := scaffoldJiraProject(t)
	t.Setenv("JIRA_API_KEY", "me@example.com:tok")

	settings := readString(t, config.ProjectSettingsPath(path))
	assert.Regexp(t, `"api_key":\s+"\$JIRA_API_KEY"`, settings)
	assert.False(t, settingsTextHasInlineSecret([]byte(settings)))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "jira", cfg.Tracker.Kind)
	assert.Equal(t, []string{"To Do", "In Progress"}, cfg.Tracker.ActiveStates)
	assert.Equal(t, "In Progress", cfg.Tracker.WorkingState)
	assert.Equal(t, "Done", cfg.Tracker.CompletionState, "a new Jira project has no review status")
	assert.Equal(t, []string{"Done"}, cfg.Tracker.TerminalStates)
	assert.Empty(t, cfg.Tracker.BacklogStates)
	assert.Empty(t, cfg.Tracker.FailedState)
	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.endpoint")

	fillJiraSetting(t, path, "endpoint", "https://example.atlassian.net")
	cfg, err = config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	require.Error(t, err, "the empty project key does not pass either")
	assert.Contains(t, err.Error(), "tracker.project_slug")

	fillJiraSetting(t, path, "project_slug", "KAN")
	cfg, err = config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	tr, err := buildTracker(cfg)
	require.NoError(t, err)
	_, ok := tr.(*jira.Client)
	assert.True(t, ok, "kind jira builds the jira client, got %T", tr)

	prompt := readString(t, path)
	assert.Contains(t, prompt, "git checkout -b {{ issue.branch_name | default: issue.identifier | downcase }}", "a Jira key has no # to strip")
	assert.Contains(t, prompt, "## Step 6 — Report")
	assert.Contains(t, prompt, "as comments on the Jira issue")
	assert.NotContains(t, prompt, "so it is visible in", "no sentence left unfinished")
	assert.NotContains(t, prompt, "gh issue comment")
	assert.NotContains(t, prompt, "api.linear.app")
}

// TestEnsureEnvStubJiraDefinesNoVariable: the Jira stub shows the form of
// JIRA_API_KEY in a comment only. A placeholder value would be loaded into
// the daemon's environment (the GITHUB_TOKEN placeholder breaks the gh CLI
// that way); a comment sets nothing. The stub still reads as not filled in,
// so init skips its first dependency analysis.
func TestEnsureEnvStubJiraDefinesNoVariable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".itervox")
	ensureEnvStub(dir, "jira")
	envPath := filepath.Join(dir, ".env")
	assert.Contains(t, readString(t, envPath), "# JIRA_API_KEY=you@example.com:")
	vars, err := godotenv.Read(envPath)
	require.NoError(t, err)
	assert.Empty(t, vars, "the stub sets no variable")
	assert.True(t, initEnvLooksLikePlaceholder(envPath), "init skips the analysis until the key is filled in")
}

// TestInitJiraNextSteps: init tells a Jira user the two settings and the
// variable to fill in, and where each one goes.
func TestInitJiraNextSteps(t *testing.T) {
	var b strings.Builder
	printInitNextSteps(&b, "jira", ".itervox", filepath.Join(".itervox", ".env"), "WORKFLOW.md")
	out := b.String()
	assert.Contains(t, out, "1. Edit "+filepath.Join(".itervox", "settings.jsonc")+": set tracker.endpoint to your Jira Cloud site URL")
	assert.Contains(t, out, "tracker.project_slug to the project key")
	assert.Contains(t, out, "2. Edit "+filepath.Join(".itervox", ".env")+": set JIRA_API_KEY=you@example.com:your_api_token")
	assert.Contains(t, out, `5. Run "itervox doctor"`)
}

// TestQuickstartTrackerFlagAcceptsJira: quickstart takes --tracker jira and
// names jira when it refuses a tracker.
func TestQuickstartTrackerFlagAcceptsJira(t *testing.T) {
	opts, err := parseQuickstartFlags([]string{"--tracker", "jira"})
	require.NoError(t, err)
	assert.Equal(t, "jira", opts.Tracker)
	_, err = parseQuickstartFlags([]string{"--tracker", "trello"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(github, linear, jira or local)")
}

// fakeJiraStatuses serves GET /rest/api/3/project/KAN/statuses: the names on
// one issue type, or status when it is not zero.
func fakeJiraStatuses(t *testing.T, status int, names ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rest/api/3/project/KAN/statuses" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		statuses := make([]map[string]string, 0, len(names))
		for i, n := range names {
			statuses = append(statuses, map[string]string{"id": strconv.Itoa(i), "name": n})
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "1", "name": "Task", "subtask": false, "statuses": statuses}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// jiraStatusConfig is a Jira tracker that sets every state field. A
// terminal "Closed" is a plain Jira status, so unlike GitHub's native closed
// it is checked.
func jiraStatusConfig(endpoint string) *config.Config {
	cfg := &config.Config{}
	cfg.Tracker.Kind = "jira"
	cfg.Tracker.Endpoint = endpoint
	cfg.Tracker.APIKey = "me@example.com:tok"
	cfg.Tracker.ProjectSlug = "KAN"
	cfg.Tracker.ActiveStates = []string{"to do", "In Progress"}
	cfg.Tracker.WorkingState = "In Progress"
	cfg.Tracker.CompletionState = "In Review"
	cfg.Tracker.TerminalStates = []string{"Done", "Closed"}
	cfg.Tracker.BacklogStates = []string{"Backlog"}
	cfg.Tracker.FailedState = "Backlog"
	return cfg
}

// TestDoctorJiraStatusCheck: for tracker.kind jira, doctor checks every
// configured state against the project's statuses, ignoring case like the
// adapter's transition lookup. Missing statuses are listed with the statuses
// the project has and fail doctor; a read error is reported, never fatal.
func TestDoctorJiraStatusCheck(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		sc := checkJiraStatuses(t.Context(), jiraStatusConfig(fakeJiraStatuses(t, 0, "To Do", "In Progress", "Done")), true)
		require.True(t, sc.Ran, sc.APIError)
		assert.Equal(t, []string{"to do", "In Progress", "In Review", "Done", "Closed", "Backlog"}, sc.Checked)
		assert.Equal(t, []string{"In Review", "Closed", "Backlog"}, sc.Missing)
		report := DoctorReport{SchemaPassed: true, JiraStatuses: sc}
		assert.Equal(t, 1, doctorExitCode(report))
		out := renderDoctorReport(report)
		assert.Contains(t, out, `ERROR: 3 configured state(s) are not statuses of Jira project KAN: "In Review", "Closed", "Backlog"`)
		assert.Contains(t, out, "  statuses in KAN: To Do, In Progress, Done\n")
		assert.Contains(t, out, "itervox config show")
	})
	t.Run("all present", func(t *testing.T) {
		sc := checkJiraStatuses(t.Context(), jiraStatusConfig(fakeJiraStatuses(t, 0, "TO DO", "In Progress", "In Review", "Done", "Backlog", "Closed")), true)
		require.True(t, sc.Ran, sc.APIError)
		assert.Empty(t, sc.Missing)
		report := DoctorReport{SchemaPassed: true, JiraStatuses: sc}
		assert.Equal(t, 0, doctorExitCode(report))
		assert.Contains(t, renderDoctorReport(report), "jira statuses: OK (6 configured states are statuses of project KAN)")
	})
	t.Run("read error", func(t *testing.T) {
		sc := checkJiraStatuses(t.Context(), jiraStatusConfig(fakeJiraStatuses(t, http.StatusUnauthorized)), true)
		assert.False(t, sc.Ran)
		assert.Contains(t, sc.APIError, "401")
		report := DoctorReport{SchemaPassed: true, JiraStatuses: sc}
		assert.Equal(t, 0, doctorExitCode(report), "a read error is reported, never fatal")
		assert.Contains(t, renderDoctorReport(report), "jira statuses: could not check project KAN: ")
	})
	t.Run("not run", func(t *testing.T) {
		assert.Equal(t, JiraStatusCheck{}, checkJiraStatuses(t.Context(), jiraStatusConfig("http://127.0.0.1:1"), false),
			"an invalid workflow: the schema error already says what is wrong")
		github := jiraStatusConfig("http://127.0.0.1:1")
		github.Tracker.Kind = "github"
		assert.Equal(t, JiraStatusCheck{}, checkJiraStatuses(t.Context(), github, true))
	})
}

// TestDoctorRunsJiraStatusCheck: collectDoctorReport runs the status check
// for a Jira workflow that validates. The site here is a closed local port,
// so the read fails, and doctor reports that without failing.
func TestDoctorRunsJiraStatusCheck(t *testing.T) {
	path := scaffoldJiraProject(t)
	t.Setenv("JIRA_API_KEY", "me@example.com:tok")
	fillJiraSetting(t, path, "endpoint", "https://127.0.0.1:1")
	fillJiraSetting(t, path, "project_slug", "KAN")

	report, _ := collectDoctorReport(path)
	require.True(t, report.SchemaPassed, report.WorkflowError)
	assert.Equal(t, "KAN", report.JiraStatuses.Project)
	assert.Equal(t, []string{"To Do", "In Progress", "Done"}, report.JiraStatuses.Checked)
	assert.NotEmpty(t, report.JiraStatuses.APIError)
	assert.Contains(t, renderDoctorReport(report), "jira statuses: could not check project KAN: ")
}

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
	assert.True(t, ok, "the jira client checks its scope on a by-ID read (retries, dispatch claim checks)")
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
