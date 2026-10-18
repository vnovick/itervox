package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

var (
	digestA = "aaaa" + strings.Repeat("1", 60)
	digestB = "aaab" + strings.Repeat("2", 60)
)

func historySnap() server.StateSnapshot {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snap := baseSnap()
	snap.History = []server.HistoryRow{
		{Identifier: "ENG-0", Status: "succeeded", StartedAt: t0}, // recorded before #186
		{Identifier: "ENG-1", Status: "succeeded", StartedAt: t0.Add(time.Hour), PromptDigest: digestA, ProfileName: "implementer", RunID: "run-1"},
		{Identifier: "ENG-2", Status: "failed", StartedAt: t0.Add(2 * time.Hour), PromptDigest: digestA, ProfileName: "implementer", RunID: "run-2",
			Steers: []server.HistorySteer{{Text: "use pnpm", At: t0.Add(2 * time.Hour), Delivery: "live"}}},
		{Identifier: "ENG-3", Status: "succeeded", StartedAt: t0.Add(3 * time.Hour), PromptDigest: digestB, ProfileName: "reviewer", RunID: "run-3"},
	}
	return snap
}

func getHistory(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	srv := server.New(makeTestConfig(historySnap()))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// TestHistoryFilterByPromptDigest (#186): GET /api/v1/history?prompt=<prefix>
// returns only the runs of the prompt version(s) whose digest starts with
// the prefix, case-insensitively, with runId, promptDigest and steers.
func TestHistoryFilterByPromptDigest(t *testing.T) {
	ids := func(path string) []string {
		w := getHistory(t, path)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var body server.HistoryResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		out := []string{}
		for _, r := range body.History {
			out = append(out, r.Identifier)
		}
		return out
	}
	assert.Equal(t, []string{"ENG-0", "ENG-1", "ENG-2", "ENG-3"}, ids("/api/v1/history"))
	assert.Equal(t, []string{"ENG-1", "ENG-2"}, ids("/api/v1/history?prompt="+digestA[:12]))
	assert.Equal(t, []string{"ENG-1", "ENG-2"}, ids("/api/v1/history?prompt="+strings.ToUpper(digestA[:12])))
	assert.Equal(t, []string{"ENG-3"}, ids("/api/v1/history?prompt="+digestB))
	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, ids("/api/v1/history?prompt=aaa"), "a prefix can match several versions")
	assert.Empty(t, ids("/api/v1/history?prompt=ffff"))

	w := getHistory(t, "/api/v1/history?prompt="+digestA)
	var raw struct {
		History []map[string]any `json:"history"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	require.Len(t, raw.History, 2)
	assert.Equal(t, "run-2", raw.History[1]["runId"])
	assert.Equal(t, digestA, raw.History[1]["promptDigest"])
	assert.NotEmpty(t, raw.History[1]["steers"])

	bad := getHistory(t, "/api/v1/history?prompt=not-hex")
	assert.Equal(t, http.StatusBadRequest, bad.Code)
	assert.Contains(t, bad.Body.String(), "invalid_prompt_digest")
}

// TestHistoryPromptVersionsGroupsOutcomes (#186): GET
// /api/v1/history/prompt-versions groups the history by prompt digest with
// run counts, per-status counts, a success rate and the profiles, newest
// version first; runs without a digest are only counted.
func TestHistoryPromptVersionsGroupsOutcomes(t *testing.T) {
	w := getHistory(t, "/api/v1/history/prompt-versions")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body server.PromptVersionsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, 1, body.UnversionedRuns)
	require.Len(t, body.Versions, 2)
	b, a := body.Versions[0], body.Versions[1]
	assert.Equal(t, digestB, b.PromptDigest, "the newest version first")
	assert.Equal(t, 1, b.Runs)
	assert.InDelta(t, 1.0, b.SuccessRate, 1e-9)
	assert.Equal(t, []string{"reviewer"}, b.Profiles)
	assert.Equal(t, digestA, a.PromptDigest)
	assert.Equal(t, 2, a.Runs)
	assert.Equal(t, map[string]int{"succeeded": 1, "failed": 1}, a.Statuses)
	assert.InDelta(t, 0.5, a.SuccessRate, 1e-9)
	assert.Equal(t, []string{"implementer"}, a.Profiles)
	assert.True(t, a.FirstSeenAt.Before(a.LastSeenAt))

	empty := server.GroupHistoryByPrompt(nil)
	assert.NotNil(t, empty.Versions, "an empty history is an empty list, not null")
}
