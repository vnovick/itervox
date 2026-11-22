package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

const (
	prA = "https://github.com/acme/app/pull/1"
	prB = "https://github.com/acme/app/pull/2"
	prC = "https://github.com/acme/app/pull/3"
)

// outcomeRows is a fixed history: two implementer runs on one merged PR
// (the second a retry), a failed implementer run with a closed PR, a
// reviewer, a run paused for input, and a run outside a 7-day window.
func outcomeRows(now time.Time) []HistoryRow {
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	return []HistoryRow{
		{Identifier: "ENG-9", Status: "succeeded", FinishedAt: at(20 * 24 * time.Hour), ProfileName: "implementer", ElapsedMs: 9000, TotalTokens: 9000},
		{Identifier: "ENG-1", Status: "failed", FinishedAt: at(3 * time.Hour), ProfileName: "implementer", Playbook: "bugfix",
			ElapsedMs: 1000, TotalTokens: 100, PRURL: prA, PROutcome: PROutcomeMerged},
		{Identifier: "ENG-1", Status: "succeeded", FinishedAt: at(2 * time.Hour), ProfileName: "implementer", Playbook: "bugfix",
			ElapsedMs: 3000, TotalTokens: 300, CacheReadTokens: 50, Attempt: 1, PRURL: prA, PROutcome: PROutcomeMerged},
		{Identifier: "ENG-2", Status: "failed", FinishedAt: at(time.Hour), ProfileName: "implementer",
			ElapsedMs: 2000, TotalTokens: 200, PRURL: prB, PROutcome: PROutcomeClosed},
		{Identifier: "ENG-1", Status: "succeeded", FinishedAt: at(90 * time.Minute), ProfileName: "reviewer", Kind: "reviewer",
			ElapsedMs: 500, TotalTokens: 50},
		{Identifier: "ENG-3", Status: "input_required", FinishedAt: at(30 * time.Minute), ProfileName: "implementer",
			ElapsedMs: 700, TotalTokens: 70, PRURL: prC, PROutcome: PROutcomeReverted},
	}
}

func groupNamed(rows []OutcomeGroupRow, name string) OutcomeGroupRow {
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	return OutcomeGroupRow{Name: "missing:" + name}
}

// TestComputeOutcomesAggregates pins the aggregate math: runs in the window
// only, success rate over finished runs (input_required excluded), retry
// rate over runs, distinct PRs by outcome, medians (even count = mean of the
// middle two), and the per-profile / per-playbook split.
func TestComputeOutcomesAggregates(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	resp := ComputeOutcomes(outcomeRows(now), now, 7*24*time.Hour, "7d")

	assert.Equal(t, "7d", resp.Window)
	assert.Equal(t, 6, resp.HistoryRuns)
	require.NotNil(t, resp.EarliestRunAt)
	assert.Equal(t, now.Add(-20*24*time.Hour), *resp.EarliestRunAt)

	tot := resp.Total
	assert.Equal(t, 5, tot.Runs, "the 20-day-old run is outside the window")
	assert.Equal(t, 4, tot.Finished)
	assert.Equal(t, 2, tot.Succeeded)
	assert.InDelta(t, 0.5, tot.SuccessRate, 1e-9)
	assert.Equal(t, 1, tot.Retries)
	assert.InDelta(t, 0.2, tot.RetryRate, 1e-9)
	assert.Equal(t, 3, tot.PRs)
	assert.Equal(t, 1, tot.PRsMerged)
	assert.Equal(t, 1, tot.PRsClosed)
	assert.Equal(t, 1, tot.PRsReverted)
	assert.Equal(t, int64(1000), tot.MedianElapsedMs) // 500 700 1000 2000 3000
	assert.Equal(t, 100, tot.MedianTokens)
	assert.Equal(t, 720, tot.TotalTokens)
	assert.Equal(t, 50, tot.CacheReadTokens)

	impl := groupNamed(resp.Profiles, "implementer")
	assert.Equal(t, 4, impl.Runs)
	assert.Equal(t, 3, impl.Finished)
	assert.Equal(t, 1, impl.Succeeded)
	assert.InDelta(t, 1.0/3.0, impl.SuccessRate, 1e-9)
	assert.Equal(t, int64(1500), impl.MedianElapsedMs, "mean of the two middle values") // 700 1000 2000 3000
	assert.Equal(t, 150, impl.MedianTokens)
	assert.Equal(t, 3, impl.PRs)
	assert.Equal(t, "implementer", resp.Profiles[0].Name, "most runs first")

	rev := groupNamed(resp.Profiles, "reviewer")
	assert.Equal(t, 1, rev.Runs)
	assert.InDelta(t, 1.0, rev.SuccessRate, 1e-9)
	assert.Zero(t, rev.PRs)

	bugfix := groupNamed(resp.Playbooks, "bugfix")
	assert.Equal(t, 2, bugfix.Runs)
	assert.Equal(t, 1, bugfix.PRs, "two runs on one PR count it once")
	assert.Equal(t, 1, bugfix.PRsMerged)
	assert.InDelta(t, 0.5, bugfix.RetryRate, 1e-9)
	none := groupNamed(resp.Playbooks, "")
	assert.Equal(t, 3, none.Runs, "runs with no playbook group under the empty name")

	empty := ComputeOutcomes(nil, now, 30*24*time.Hour, "30d")
	assert.Zero(t, empty.Total.Runs)
	assert.Zero(t, empty.Total.SuccessRate)
	assert.NotNil(t, empty.Profiles)
	assert.Nil(t, empty.EarliestRunAt)
}

// TestIssueOutcomeSumsTheIssuesRuns: the issue detail figures sum the
// issue's runs and name the newest PR's outcome.
func TestIssueOutcomeSumsTheIssuesRuns(t *testing.T) {
	now := time.Now()
	rows := outcomeRows(now)
	rows[2].TurnCount, rows[2].InputTokens, rows[2].OutputTokens, rows[2].CacheWriteTokens = 4, 200, 100, 30
	got := IssueOutcome(rows, "ENG-1")
	require.NotNil(t, got)
	assert.Equal(t, 3, got.Runs)
	assert.Equal(t, 2, got.Succeeded)
	assert.Equal(t, 1, got.Retries)
	assert.Equal(t, 4, got.Turns)
	assert.Equal(t, int64(4500), got.ElapsedMs)
	assert.Equal(t, 200, got.InputTokens)
	assert.Equal(t, 30, got.CacheWriteTokens)
	assert.Equal(t, "succeeded", got.LastStatus)
	assert.Equal(t, prA, got.PRURL)
	assert.Equal(t, PROutcomeMerged, got.PROutcome)
	assert.Nil(t, IssueOutcome(rows, "ENG-404"))
}

func TestParseOutcomesWindow(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 30 * 24 * time.Hour, "7d": 7 * 24 * time.Hour, "36h": 36 * time.Hour, "90d": 90 * 24 * time.Hour} {
		got, ok := ParseOutcomesWindow(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"0d", "91d", "-1d", "xd", "10m", "abc", "1y"} {
		_, ok := ParseOutcomesWindow(bad)
		assert.False(t, ok, bad)
	}
}

// TestOutcomesHandlerAtViewerScope: GET /api/v1/outcomes is a viewer route
// that serves the aggregates of the snapshot's history and refuses a bad
// window with 400; without a token it is 401.
func TestOutcomesHandlerAtViewerScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", apitokens.FileName)
	store := apitokens.Open(path)
	_, viewer, err := store.Create("viewer-token", apitokens.ScopeViewer, nil, "test")
	require.NoError(t, err)
	now := time.Now()
	srv := New(Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{History: outcomeRows(now)} },
		RefreshChan: make(chan struct{}, 1),
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
		Metrics:     http.NotFoundHandler(),
	})
	assert.Equal(t, accessViewer, routeScopes["GET /api/v1/outcomes"])

	rec := doReq(t, srv, http.MethodGet, "/api/v1/outcomes?window=7d", viewer)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body OutcomesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "7d", body.Window)
	assert.Equal(t, 5, body.Total.Runs)
	assert.Equal(t, "implementer", body.Profiles[0].Name)

	rec = doReq(t, srv, http.MethodGet, "/api/v1/outcomes", viewer)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "30d", body.Window)
	assert.Equal(t, 6, body.Total.Runs)

	rec = doReq(t, srv, http.MethodGet, "/api/v1/outcomes?window=1y", viewer)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_window")

	rec = doReq(t, srv, http.MethodGet, "/api/v1/outcomes", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
