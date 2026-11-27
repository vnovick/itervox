package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// TestBuildNeedsYouOldestFirstWithKinds pins the lane's shape: only rows
// that wait on a person (not an answered, resuming one), oldest first with
// unknown times last, the kind from the approval flags, the title from the
// map and a one-line summary.
func TestBuildNeedsYouOldestFirstWithKinds(t *testing.T) {
	rows := []InputRequiredRow{
		{Identifier: "ENG-3", State: "input_required", SessionID: "s3", QueuedAt: "2026-10-22T10:30:00Z",
			Context: "Agent wants to run: `npm publish`\n\nThis command is in guardrails.approval_commands.", GuardrailApproval: true},
		{Identifier: "ENG-1", State: "input_required", SessionID: "s1", QueuedAt: "2026-10-22T09:00:00Z",
			Context: "\n<!-- itervox:needs-input -->\nWhich database should I use?\nMore detail."},
		{Identifier: "ENG-2", State: "pending_input_resume", QueuedAt: "2026-10-22T08:00:00Z", Context: "Reply received"},
		{Identifier: "ENG-4", State: "input_required", QueuedAt: "0001-01-01T00:00:00Z", Context: "Old?"},
		{Identifier: "ENG-5", State: "input_required", QueuedAt: "2026-10-22T10:00:00Z", GuardrailApproval: true,
			GuardrailLanePaths: []string{".github/workflows/ci.yml"}, GuardrailLanePathsTotal: 1,
			Context: "Pipeline change: the agent wants to push changes to `.github/workflows/ci.yml` (`git push`)\n\nThese files…"},
	}
	got := BuildNeedsYou(rows, map[string]string{"ENG-1": "Pick a\ndatabase"}, "web-app")

	ids := make([]string, len(got))
	for i, r := range got {
		ids[i] = r.Identifier
	}
	assert.Equal(t, []string{"ENG-1", "ENG-5", "ENG-3", "ENG-4"}, ids, "oldest first, unknown time last, resuming left out")
	assert.Equal(t, NeedsYouRow{Kind: NeedsYouQuestion, Identifier: "ENG-1", Title: "Pick a database", Project: "web-app",
		SessionID: "s1", Summary: "Which database should I use?", WaitingSince: "2026-10-22T09:00:00Z"}, got[0])
	assert.Equal(t, NeedsYouPipelineChange, got[1].Kind)
	assert.Equal(t, "Pipeline change: the agent wants to push changes to `.github/workflows/ci.yml` (`git push`)", got[1].Summary)
	assert.Equal(t, NeedsYouApproval, got[2].Kind)
	assert.Equal(t, "Agent wants to run: `npm publish`", got[2].Summary)
	assert.Empty(t, got[3].WaitingSince, "a zero queued time is unknown, not year 1")

	assert.Empty(t, BuildNeedsYou(nil, nil, ""))
}

// TestNeedsYouSummaryIsRedactedAndBounded: the summary is safe for a viewer
// — a secret in the agent's text is redacted before the cut, control
// characters (terminal escapes) are dropped and the line is capped.
func TestNeedsYouSummaryIsRedactedAndBounded(t *testing.T) {
	secret := "sk-ant-abcdefghijklmnopqrstuvwxyz0123456789"
	s := needsYouSummary("Use ANTHROPIC_API_KEY=" + secret + " \x1b[31mnow\x1b[0m?")
	assert.NotContains(t, s, secret)
	assert.Contains(t, s, "ANTHROPIC_API_KEY=")
	assert.NotContains(t, s, "\x1b")

	long := needsYouSummary(strings.Repeat("é", 500))
	assert.Equal(t, needsYouSummaryRunes, len([]rune(long)))
	assert.True(t, strings.HasSuffix(long, "…"))
}

// TestNeedsYouReachesAViewer: GET /api/v1/state and GET /api/v1/summary
// (both viewer routes) carry the lane to a viewer token, and the summary
// keeps at most summaryNeedsYouMax rows, the oldest.
func TestNeedsYouReachesAViewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", apitokens.FileName)
	store := apitokens.Open(path)
	_, viewer, err := store.Create("viewer-token", apitokens.ScopeViewer, nil, "test")
	require.NoError(t, err)
	var input []InputRequiredRow
	for i := range summaryNeedsYouMax + 5 {
		input = append(input, InputRequiredRow{Identifier: "ENG-" + string(rune('A'+i)), State: "input_required",
			Context: "Q?", QueuedAt: "2026-10-22T10:" + twoDigits(i) + ":00Z"})
	}
	lane := BuildNeedsYou(input, nil, "web-app")
	srv := New(Config{
		Snapshot: func() StateSnapshot {
			return StateSnapshot{ProjectName: "web-app", InputRequired: input, NeedsYou: lane}
		},
		RefreshChan: make(chan struct{}, 1),
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
		Metrics:     http.NotFoundHandler(),
	})

	rec := doReq(t, srv, http.MethodGet, "/api/v1/state", viewer)
	require.Equal(t, http.StatusOK, rec.Code)
	var snap StateSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	require.Len(t, snap.NeedsYou, summaryNeedsYouMax+5)
	assert.Equal(t, "ENG-A", snap.NeedsYou[0].Identifier)

	rec = doReq(t, srv, http.MethodGet, "/api/v1/summary", viewer)
	require.Equal(t, http.StatusOK, rec.Code)
	var sum Summary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sum))
	require.Len(t, sum.NeedsYou, summaryNeedsYouMax)
	assert.Equal(t, "ENG-A", sum.NeedsYou[0].Identifier, "the summary keeps the oldest")
	assert.Equal(t, summaryNeedsYouMax+5, sum.Counts.InputRequired, "the count stays exact")
}

func twoDigits(i int) string {
	return string([]byte{byte('0' + i/10), byte('0' + i%10)})
}
