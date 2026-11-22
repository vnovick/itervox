//go:build unix

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
)

// The evidence command tests use the unix-only fake daemon
// (client_commands_test.go), like the other client command tests.

func evidenceDaemon(t *testing.T, rep server.EvidenceReport) *fakeDaemon {
	t.Helper()
	return newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/issues/ENG-7/evidence", r.URL.EscapedPath())
		writeJSONBody(w, rep)
	})
}

// TestEvidenceCommandText: `itervox evidence ENG-7` prints the runs, the
// checks, the approvals and the PRs from the daemon's report.
func TestEvidenceCommandText(t *testing.T) {
	rep := buildIssueEvidence(evidenceInputs{identifier: "ENG-7", state: evidenceState(), history: evidenceHistory(),
		profiles: map[string]config.AgentProfile{"coder": {Command: "claude --model claude-sonnet-4-5"}}, now: evT0.Add(2 * time.Hour)})
	rep.Scope = "operator"
	d := evidenceDaemon(t, rep)
	code, out, errOut := runClient(t, t.Context(), d, "evidence", "ENG-7")
	require.Equal(t, 0, code, errOut)
	for _, want := range []string{
		"Evidence for ENG-7 — Fix the parser (as operator)",
		"Runs (3)",
		"run run-2  claude · claude-sonnet-4-5  0 turns  30m0s",
		"prompt bbbbbbbbbbbb",
		"memory [m1]",
		"playbook bugfix",
		"evidence gate [tests, ci]: passed",
		"evidence gate [tests]: FAILED: tests",
		"signed checks [lint]: passed",
		"retry attempt 1",
		"steering: 1 delivered (0 older dropped)",
		"live: check the tests",
		"retry 2 due 2026-11-20 12:00Z",
		"review reviewer: approve on codex (1 reasons, 2 line comments)",
		"review quorum any_block: passed (1 approve, 0 block)",
		"guardrail refused force_push",
		"push lane human: 3 files, 1 kept (writers: claude)",
		"Approvals (4)",
		"input answered  tracker · tracker:Dana",
		"guardrail push approved  dashboard · dashboard:tok_op",
		"guardrail rejected  slack · dashboard:tok_hub",
		"guardrail push pending",
		"Pull requests (1)",
		"https://github.com/o/r/pull/9  merged",
		"the feed keeps 168 h",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "secret finding text")
}

// TestEvidenceCommandJSON: --json prints the daemon's response as it came.
func TestEvidenceCommandJSON(t *testing.T) {
	rep := server.EvidenceReport{Identifier: "ENG-7", Scope: "viewer", Runs: []server.EvidenceRun{{RunID: "run-1", Kind: "worker", Status: "failed"}}}
	d := evidenceDaemon(t, rep)
	code, out, errOut := runClient(t, t.Context(), d, "evidence", "ENG-7", "--json")
	require.Equal(t, 0, code, errOut)
	var got server.EvidenceReport
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "ENG-7", got.Identifier)
	require.Len(t, got.Runs, 1)
	assert.Equal(t, "run-1", got.Runs[0].RunID)
	assert.Equal(t, 1, strings.Count(out, "\n"), "one JSON document, one line")
}

func TestEvidenceCommandUsageAndRefusal(t *testing.T) {
	d := newFakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"not_supported","message":"the evidence report is not available"}}`))
	})
	code, _, errOut := runClient(t, t.Context(), d, "evidence")
	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "usage: itervox evidence <issue> [--json]")
	code, _, errOut = runClient(t, t.Context(), d, "evidence", "ENG-7")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "itervox evidence:")
}
