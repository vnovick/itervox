package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/evals"
	"github.com/vnovick/itervox/internal/server"
)

// TestRepoEvalsEndpointServesPersistedReports (#201): GET /api/v1/repo/evals
// serves the latest persisted result per case from the project's
// .itervox/evals, with failed assertions, the prompt digest and the
// regression marker.
func TestRepoEvalsEndpointServesPersistedReports(t *testing.T) {
	project := t.TempDir()
	workflow := filepath.Join(project, "WORKFLOW.md")
	dir := repoEvalsDir(workflow)
	assert.Equal(t, filepath.Join(project, ".itervox", "evals"), dir)

	prev, cur := strings.Repeat("a", 64), strings.Repeat("b", 64)
	t0 := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	pass := evals.ScenarioVerdict{Profile: "reviewer", Scenario: "rejects",
		Deterministic: evals.JudgeVerdict{Pass: true}, Structural: evals.JudgeVerdict{Pass: true},
		PromptDigest: prev, DigestSource: evals.DigestSourceLive}
	fail := pass
	fail.PromptDigest = cur
	fail.Structural = evals.JudgeVerdict{Reason: "forbidden phrase present in comments: /ai-approved",
		Failures: []string{"forbidden phrase present in comments: /ai-approved"}}
	for i, v := range []evals.ScenarioVerdict{pass, fail} {
		at := t0.Add(time.Duration(i) * time.Hour)
		_, err := evals.WriteResultFile(dir, evals.NewResultFile(evals.ModeLive, evals.Report{Verdicts: []evals.ScenarioVerdict{v}}, nil, "", at, at))
		require.NoError(t, err)
	}

	srv := server.New(server.Config{
		Snapshot:     func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		AllowedHosts: []string{"example.com"},
		RepoEvals:    repoEvalsProvider(workflow),
	})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/repo/evals", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body server.RepoEvalsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Reports, 2)
	require.Len(t, body.Cases, 1)
	c := body.Cases[0]
	assert.False(t, c.Pass)
	assert.Equal(t, cur, c.PromptDigest)
	assert.True(t, c.Regression)
	assert.Equal(t, prev, c.PreviousDigest)
	assert.Equal(t, []string{"forbidden phrase present in comments: /ai-approved"}, c.Failures)
	assert.Equal(t, evals.ModeLive, c.Mode)
	assert.True(t, c.RanAt.Equal(t0.Add(time.Hour)))

	// No reports yet: an empty list, not an error.
	empty := buildRepoEvals(filepath.Join(t.TempDir(), ".itervox", "evals"))
	assert.Empty(t, empty.Error)
	assert.NotNil(t, empty.Cases)
}
