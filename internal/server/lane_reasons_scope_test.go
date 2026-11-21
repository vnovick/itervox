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
	"github.com/vnovick/itervox/internal/guardrails"
)

// heldPushReason is a finding whose added line holds a token.
func heldPushReason(secret string) guardrails.LaneReason {
	return guardrails.LaneReason{Kind: guardrails.ReasonFinding, Path: ".github/workflows/ci.yml", Line: 3,
		Rule: "ci_continue_on_error", Message: "failure ignored in a CI workflow", Text: "continue-on-error: true # " + secret}
}

// The snapshot's input-required row of a held push (GET /api/v1/state,
// the SSE frame, the hub proxy): a viewer never gets a finding's added
// line, an operator gets it redacted (the approval card), an admin as the
// daemon recorded it; the shared snapshot is not modified in place.
func TestSnapshotInputRequiredLaneReasonTextNeedsOperator(t *testing.T) {
	secret := "ghp_" + strings.Repeat("Q", 36)
	rows := []InputRequiredRow{{Identifier: "ENG-1", GuardrailApproval: true,
		GuardrailLanePaths: []string{".github/workflows/ci.yml"}, GuardrailLanePathsTotal: 1,
		GuardrailLaneReasons: []guardrails.LaneReason{heldPushReason(secret)}, GuardrailLaneReasonsTotal: 1}}
	store := apitokens.Open(filepath.Join(t.TempDir(), ".itervox", apitokens.FileName))
	secrets := map[apitokens.Scope]string{}
	for _, sc := range apitokens.Scopes {
		_, s, err := store.Create(string(sc)+"-token", sc, nil, "test")
		require.NoError(t, err)
		secrets[sc] = s
	}
	srv := New(Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{InputRequired: rows} },
		RefreshChan: make(chan struct{}, 1),
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
	})
	get := func(sc apitokens.Scope) InputRequiredRow {
		rec := doReq(t, srv, http.MethodGet, "/api/v1/state", secrets[sc])
		require.Equal(t, http.StatusOK, rec.Code, sc)
		var snap StateSnapshot
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
		require.Len(t, snap.InputRequired, 1)
		require.Len(t, snap.InputRequired[0].GuardrailLaneReasons, 1)
		assert.NotContains(t, rec.Body.String(), "continue-on-error: true", "%s: nowhere in the body", sc)
		return snap.InputRequired[0]
	}
	viewer := get(apitokens.ScopeViewer)
	assert.Empty(t, viewer.GuardrailLaneReasons[0].Text, "a viewer never gets the added line")
	assert.Equal(t, 3, viewer.GuardrailLaneReasons[0].Line, "location and rule stay")
	assert.Equal(t, "ci_continue_on_error", viewer.GuardrailLaneReasons[0].Rule)

	rec := doReq(t, srv, http.MethodGet, "/api/v1/state", secrets[apitokens.ScopeOperator])
	var snap StateSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	op := snap.InputRequired[0].GuardrailLaneReasons[0].Text
	assert.Contains(t, op, "continue-on-error: true", "an operator sees the line")
	assert.NotContains(t, op, secret, "redacted below admin")

	rec = doReq(t, srv, http.MethodGet, "/api/v1/state", secrets[apitokens.ScopeAdmin])
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	assert.Contains(t, snap.InputRequired[0].GuardrailLaneReasons[0].Text, secret)
	assert.Contains(t, rows[0].GuardrailLaneReasons[0].Text, secret, "the shared rows are not modified in place")
}

// The run context (GET /api/v1/issues/{identifier}/context, viewer scope,
// read by Slack) carries a held push's reasons only as text lines built by
// LaneReason.String, which never includes the added line.
func TestRunContextPendingReasonsCarryNoAddedLine(t *testing.T) {
	secret := "ghp_" + strings.Repeat("Q", 36)
	r := heldPushReason(secret)
	ch := &fakeChanges{run: &RunContextRow{RunID: "r1", Live: true, PendingApproval: true,
		PendingReasons: guardrails.LaneReasonLines([]guardrails.LaneReason{r}, 1, guardrails.MaxLaneReasonsShown)}}
	srv, secrets := newChangesServer(t, ch)
	for _, sc := range apitokens.Scopes {
		rec := doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/context", secrets[sc])
		require.Equal(t, http.StatusOK, rec.Code, sc)
		var resp RunContextResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.NotNil(t, resp.Run)
		assert.Equal(t, []string{".github/workflows/ci.yml:3: failure ignored in a CI workflow [ci_continue_on_error]"}, resp.Run.PendingReasons, sc)
		assert.NotContains(t, rec.Body.String(), "continue-on-error: true", sc)
		assert.NotContains(t, rec.Body.String(), secret, sc)
	}
}
