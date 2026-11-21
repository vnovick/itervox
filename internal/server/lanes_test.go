package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/guardrails"
)

// fakeLanes is a ChangesClient that also serves the Lanes view.
type fakeLanes struct {
	fakeChanges
	lanes    LanesResponse
	lanesErr error
}

func (f *fakeLanes) Lanes(context.Context, string) (LanesResponse, error) {
	return f.lanes, f.lanesErr
}

// TestLanesEndpointShapeAndScope: every scope reads the Lanes view (files
// by lane with their rule, findings per file with line, the reasons, the
// stack split); a finding's line text is a hunk line, so a viewer gets it
// withheld, an operator redacted, an admin as the daemon recorded it; a
// daemon whose changes client has no lanes view answers 503, and errors
// map like the diff's.
func TestLanesEndpointShapeAndScope(t *testing.T) {
	secret := "ghp_" + strings.Repeat("Q", 36)
	finding := guardrails.LaneReason{Kind: guardrails.ReasonFinding, Path: ".github/workflows/ci.yml", Line: 10,
		Rule: "ci_continue_on_error", Message: "failure ignored in a CI workflow", Text: "continue-on-error: true # " + secret}
	ch := &fakeLanes{lanes: LanesResponse{Base: "origin/main", MergeBase: "abc", Enforced: true, Lane: guardrails.LaneHuman,
		Files: []LaneFileRow{
			{DiffFileRow: DiffFileRow{Path: ".github/workflows/ci.yml", Status: "modified", Additions: 1}, Lane: guardrails.LaneHuman,
				Rule: ".github/**", Findings: []guardrails.LaneReason{finding}},
			{DiffFileRow: DiffFileRow{Path: "main.go", Status: "modified", Additions: 2}, Lane: guardrails.LaneAuto},
		},
		Reasons:      []guardrails.LaneReason{finding, {Kind: guardrails.ReasonLaneRule, Path: ".github/workflows/ci.yml", Lane: guardrails.LaneHuman, Rule: ".github/**"}},
		ReasonsTotal: 2,
		Split: &StackSplitRow{Order: "code_first", Reason: "the code passed its checks alone", CodeFiles: []string{"main.go"}, CodeFilesTotal: 1,
			PipelineFiles: []string{".github/workflows/ci.yml"}, PipelineFilesTotal: 1, LowerPR: 7, LowerPRURL: "https://example.test/pr/7"},
		RecordedLane: guardrails.LaneHuman, ApprovalPending: true,
	}}
	srv, secrets := newChangesServer(t, ch)

	get := func(sc apitokens.Scope) (LanesResponse, map[string]any) {
		rec := doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/lanes", secrets[sc])
		require.Equal(t, http.StatusOK, rec.Code, sc)
		var resp LanesResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		var raw map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		return resp, raw
	}

	viewer, raw := get(apitokens.ScopeViewer)
	assert.Equal(t, "ENG-1", viewer.Identifier)
	assert.Equal(t, guardrails.LaneHuman, viewer.Lane)
	require.Len(t, viewer.Files, 2)
	assert.Equal(t, ".github/**", viewer.Files[0].Rule)
	require.Len(t, viewer.Files[0].Findings, 1)
	assert.Equal(t, 10, viewer.Files[0].Findings[0].Line)
	assert.Empty(t, viewer.Files[0].Findings[0].Text, "a viewer never gets the added line")
	assert.Empty(t, viewer.Reasons[0].Text)
	assert.False(t, viewer.TextAllowed)
	assert.Equal(t, []any{}, raw["files"].([]any)[1].(map[string]any)["findings"], "an empty list, never null")
	require.NotNil(t, viewer.Split)
	assert.Equal(t, "code_first", viewer.Split.Order)
	assert.Equal(t, 7, viewer.Split.LowerPR)
	assert.True(t, viewer.ApprovalPending)
	assert.Equal(t, guardrails.LaneHuman, viewer.RecordedLane)

	operator, _ := get(apitokens.ScopeOperator)
	assert.True(t, operator.TextAllowed)
	assert.Contains(t, operator.Files[0].Findings[0].Text, "continue-on-error: true")
	assert.NotContains(t, operator.Files[0].Findings[0].Text, secret, "operator text is redacted")
	assert.NotContains(t, operator.Reasons[0].Text, secret)

	admin, _ := get(apitokens.ScopeAdmin)
	assert.Contains(t, admin.Files[0].Findings[0].Text, secret, "admins see what the daemon recorded")
	assert.Contains(t, ch.lanes.Files[0].Findings[0].Text, secret, "the client's data is not modified in place")

	for err, want := range map[error]int{
		ErrNoWorktree:            http.StatusNotFound,
		ErrDiffBaseNotFound:      http.StatusNotFound,
		context.DeadlineExceeded: http.StatusGatewayTimeout,
	} {
		ch.lanesErr = err
		rec := doReq(t, srv, http.MethodGet, "/api/v1/issues/ENG-1/lanes", secrets[apitokens.ScopeViewer])
		assert.Equal(t, want, rec.Code, err.Error())
	}

	plain, plainSecrets := newChangesServer(t, &fakeChanges{})
	rec := doReq(t, plain, http.MethodGet, "/api/v1/issues/ENG-1/lanes", plainSecrets[apitokens.ScopeViewer])
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
