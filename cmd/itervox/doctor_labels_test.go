package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// fakeLabelRepo is a minimal GitHub labels API for owner/repo.
type fakeLabelRepo struct {
	mu       sync.Mutex
	labels   []string
	created  []string
	requests int
	listErr  int // non-zero: GET /labels answers with this status
	// createFailAfter > 0: POSTs after that many successes fail with 500.
	createFailAfter int
	// listFailAfterCreate: GET /labels fails once a POST has been made.
	listFailAfterCreate bool
	posts               int
}

func (f *fakeLabelRepo) serve(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		if r.URL.Path != "/repos/owner/repo/labels" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if f.listErr != 0 || (f.listFailAfterCreate && f.posts > 0) {
				w.WriteHeader(cmp.Or(f.listErr, http.StatusBadGateway))
				_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
				return
			}
			out := make([]map[string]any, 0, len(f.labels))
			for _, n := range f.labels {
				out = append(out, map[string]any{"name": n})
			}
			_ = json.NewEncoder(w).Encode(out)
		case http.MethodPost:
			f.posts++
			if f.createFailAfter > 0 && f.posts > f.createFailAfter {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
				return
			}
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.labels = append(f.labels, in["name"])
			f.created = append(f.created, in["name"]+"#"+in["color"])
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": in["name"]})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

// writeLabelWorkflow writes a GitHub workflow whose tracker endpoint is the
// fake API. States mirror this repository's own WORKFLOW.md, plus "closed"
// (GitHub's native state, never a label) and a case-variant duplicate.
func writeLabelWorkflow(t *testing.T, kind, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	content := fmt.Sprintf(`---
itervox_schema_version: 2
tracker:
  kind: %s
  api_key: test-token
  project_slug: owner/repo
  endpoint: %s
  active_states: ["todo", "in-progress"]
  terminal_states: ["done", "cancelled", "closed"]
  working_state: "In-Progress"
  completion_state: "in-review"
  backlog_states: ["backlog"]
---

Prompt.
`, kind, endpoint)
	require.NoError(t, os.WriteFile(wf, []byte(content), 0o644))
	return wf
}

var wantStateLabels = []string{"todo", "in-progress", "in-review", "done", "cancelled", "backlog"}

func TestGitHubStateLabelsDedupesAndSkipsClosed(t *testing.T) {
	got := githubStateLabels(config.TrackerConfig{
		ActiveStates:    []string{"todo", "in-progress"},
		WorkingState:    "In-Progress",
		CompletionState: "in-review",
		TerminalStates:  []string{"done", "Closed", ""},
		BacklogStates:   []string{"backlog"},
		FailedState:     "failed",
	})
	assert.Equal(t, []string{"todo", "in-progress", "in-review", "done", "backlog", "failed"}, got)
}

// TestGitHubStateLabelsKeepsClosedOutsideTerminalStates (#75 review): only a
// terminal "closed" is GitHub's native state. Active states are fetched as
// literal labels and working/completion/backlog/failed states are written as
// labels, so a "closed" there is a label that must exist.
func TestGitHubStateLabelsKeepsClosedOutsideTerminalStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.TrackerConfig
	}{
		{"active", config.TrackerConfig{ActiveStates: []string{"closed"}}},
		{"working", config.TrackerConfig{WorkingState: "closed"}},
		{"completion", config.TrackerConfig{CompletionState: "Closed"}},
		{"backlog", config.TrackerConfig{BacklogStates: []string{"closed"}}},
		{"failed", config.TrackerConfig{FailedState: "closed"}},
	} {
		got := githubStateLabels(tc.cfg)
		require.Len(t, got, 1, tc.name)
		assert.True(t, strings.EqualFold(got[0], "closed"), tc.name)
	}
	assert.Empty(t, githubStateLabels(config.TrackerConfig{TerminalStates: []string{"closed"}}), "terminal: native state")
}

// TestDoctorGitHubLabelsAllPresent: every configured state label exists
// (one only differs in case, which GitHub treats as the same label).
func TestDoctorGitHubLabelsAllPresent(t *testing.T) {
	repo := &fakeLabelRepo{labels: []string{"bug", "TODO", "in-progress", "in-review", "done", "cancelled", "backlog"}}
	wf := writeLabelWorkflow(t, "github", repo.serve(t).URL)

	report, _ := collectDoctorReport(wf)
	assert.True(t, report.Labels.Ran)
	assert.Equal(t, wantStateLabels, report.Labels.Checked)
	assert.Empty(t, report.Labels.Missing)
	out := renderDoctorReport(report)
	assert.Contains(t, out, "github labels: OK (6 state labels present on owner/repo)")
	assert.NotContains(t, out, "GitHub setup guide", "no guide link when nothing is wrong")
}

// TestDoctorGitHubLabelsMissing pins #75: every missing state label is listed
// with its exact `gh label create` command, and doctor exits 1.
func TestDoctorGitHubLabelsMissing(t *testing.T) {
	repo := &fakeLabelRepo{labels: []string{"todo", "done"}}
	wf := writeLabelWorkflow(t, "github", repo.serve(t).URL)

	report, _ := collectDoctorReport(wf)
	assert.Equal(t, []string{"in-progress", "in-review", "cancelled", "backlog"}, report.Labels.Missing)
	out := renderDoctorReport(report)
	assert.Contains(t, out, "ERROR: 4 state label(s) missing on owner/repo")
	for _, line := range []string{
		`gh label create "in-progress" --color "e4e669" --repo owner/repo`,
		`gh label create "in-review" --color "d93f0b" --repo owner/repo`,
		`gh label create "cancelled" --color "cccccc" --repo owner/repo`,
		`gh label create "backlog" --color "f9f9f9" --repo owner/repo`,
	} {
		assert.Contains(t, out, line)
	}
	assert.NotContains(t, out, `gh label create "closed"`)
	assert.Contains(t, out, "GitHub setup guide (labels, priority, blockers, gh auth): https://itervox.dev/guides/github-issues/")
	assert.Equal(t, 1, doctorExitCode(DoctorReport{SchemaPassed: true, Labels: report.Labels}))
}

// TestDoctorGitHubLabelsAPIErrorIsReportedNotFatal: an unreachable or
// rejecting API is shown, with the token redacted, and never fails doctor.
func TestDoctorGitHubLabelsAPIErrorIsReportedNotFatal(t *testing.T) {
	repo := &fakeLabelRepo{listErr: http.StatusUnauthorized}
	wf := writeLabelWorkflow(t, "github", repo.serve(t).URL)

	report, _ := collectDoctorReport(wf)
	assert.False(t, report.Labels.Ran)
	assert.NotEmpty(t, report.Labels.APIError)
	assert.Empty(t, report.Labels.Missing)
	out := renderDoctorReport(report)
	assert.Contains(t, out, "github labels: could not check owner/repo")
	assert.Contains(t, out, githubSetupGuideURL)
	assert.NotContains(t, out, "test-token")
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, Labels: report.Labels}))

	// Unreachable host: same outcome.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	report, _ = collectDoctorReport(writeLabelWorkflow(t, "github", deadURL))
	assert.NotEmpty(t, report.Labels.APIError)
	assert.Equal(t, 0, doctorExitCode(DoctorReport{SchemaPassed: true, Labels: report.Labels}))
}

// TestDoctorLabelCheckSkipsLinear: a Linear workflow makes no label request
// and prints nothing about labels.
func TestDoctorLabelCheckSkipsLinear(t *testing.T) {
	repo := &fakeLabelRepo{}
	wf := writeLabelWorkflow(t, "linear", repo.serve(t).URL)

	report, _ := collectDoctorReport(wf)
	assert.Equal(t, LabelCheck{}, report.Labels)
	assert.Equal(t, 0, repo.requests)
	assert.NotContains(t, renderDoctorReport(report), "github labels")
}

func TestDoctorGitHubLabelsSkippedWithoutToken(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.Kind = "github"
	cfg.Tracker.ProjectSlug = "owner/repo"
	cfg.Tracker.ActiveStates = []string{"todo"}
	lc := checkGitHubLabels(context.Background(), cfg)
	assert.False(t, lc.Ran)
	assert.Contains(t, lc.SkipReason, "tracker.api_key is empty")
}

// TestDoctorFixCreatesMissingLabels: `doctor --fix --yes` creates exactly the
// missing labels with their colours, and the re-check then passes.
func TestDoctorFixCreatesMissingLabels(t *testing.T) {
	repo := &fakeLabelRepo{labels: []string{"todo", "done"}}
	wf := writeLabelWorkflow(t, "github", repo.serve(t).URL)

	var out strings.Builder
	report, _ := runDoctorFix(wf, true, strings.NewReader(""), &out)
	assert.Equal(t, []string{"in-progress#e4e669", "in-review#d93f0b", "cancelled#cccccc", "backlog#f9f9f9"}, repo.created)
	assert.Contains(t, out.String(), `created label "in-progress" on owner/repo`)
	assert.Contains(t, report, "github labels: OK (6 state labels present on owner/repo)")
}

// TestDoctorFixAsksBeforeCreating: without --yes, only y/yes creates; any
// other answer, including EOF from a non-interactive stdin, creates nothing.
func TestDoctorFixAsksBeforeCreating(t *testing.T) {
	for _, answer := range []string{"", "n\n", "no\n", "maybe\n"} {
		repo := &fakeLabelRepo{labels: []string{"todo"}}
		wf := writeLabelWorkflow(t, "github", repo.serve(t).URL)
		var out strings.Builder
		report, _ := runDoctorFix(wf, false, strings.NewReader(answer), &out)
		assert.Empty(t, repo.created, "answer %q must not create labels", answer)
		assert.Contains(t, out.String(), "Create 5 label(s) on owner/repo")
		assert.Contains(t, out.String(), "no labels created (pass --yes")
		assert.Contains(t, report, "ERROR: 5 state label(s) missing")
	}

	repo := &fakeLabelRepo{labels: []string{"todo"}}
	wf := writeLabelWorkflow(t, "github", repo.serve(t).URL)
	var out strings.Builder
	report, _ := runDoctorFix(wf, false, strings.NewReader("y\n"), &out)
	assert.Len(t, repo.created, 5)
	assert.Contains(t, report, "github labels: OK")
}

// slowYes answers "y" only after delay, like a person reading the prompt.
type slowYes struct {
	delay time.Duration
	done  bool
}

func (r *slowYes) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	r.done = true
	return copy(p, "y\n"), nil
}

// TestDoctorFixDeadlineStartsAfterTheAnswer (#75 review): the API deadline
// starts once the operator has answered, so a slow "y" still creates the
// labels instead of failing with an expired context.
func TestDoctorFixDeadlineStartsAfterTheAnswer(t *testing.T) {
	old := labelCheckTimeout
	labelCheckTimeout = 300 * time.Millisecond
	t.Cleanup(func() { labelCheckTimeout = old })
	repo := &fakeLabelRepo{labels: []string{"todo"}}
	wf := writeLabelWorkflow(t, "github", repo.serve(t).URL)

	var out strings.Builder
	report, code := runDoctorFix(wf, false, &slowYes{delay: 600 * time.Millisecond}, &out)
	assert.NotContains(t, out.String(), "deadline exceeded")
	assert.Len(t, repo.created, 5)
	assert.Contains(t, report, "github labels: OK")
	assert.Equal(t, 0, code)
}

// TestDoctorFixPartialFailureStaysNonZero (#75 review): a repair that created
// some labels and then failed, followed by a re-check that also failed, exits
// 1 and still reports the labels that were not created, instead of an empty
// missing list and exit 0.
func TestDoctorFixPartialFailureStaysNonZero(t *testing.T) {
	repo := &fakeLabelRepo{labels: []string{"todo"}, createFailAfter: 1, listFailAfterCreate: true}
	wf := writeLabelWorkflow(t, "github", repo.serve(t).URL)

	var out strings.Builder
	report, code := runDoctorFix(wf, true, strings.NewReader(""), &out)
	require.Len(t, repo.created, 1)
	assert.Contains(t, out.String(), "ERROR: create label")
	assert.Contains(t, report, "ERROR: 4 state label(s) missing")
	assert.Contains(t, report, "label repair incomplete")
	assert.Equal(t, 1, code)
}
