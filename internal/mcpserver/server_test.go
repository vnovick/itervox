package mcpserver

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var readToolNames = []string{"board_summary", "list_issues", "get_issue", "explain_blockers", "recent_failures", "feed_since", "recall"}
var writeToolNames = []string{"dispatch", "pause_dispatch", "resume_dispatch", "reply_input", "steer", "remember"}

// TestMCPListsToolsByScope (#216): a viewer token lists the read tools and
// no write tool, and a write tool called anyway is refused before it
// reaches the daemon; an operator token lists both; MaxScope caps an admin
// caller (a daemon without authentication) to the read tools.
func TestMCPListsToolsByScope(t *testing.T) {
	cases := []struct {
		name, scope, maxScope string
		want                  []string
	}{
		{"viewer", "viewer", "", readToolNames},
		{"operator", "operator", "", append(append([]string{}, readToolNames...), writeToolNames...)},
		{"admin capped to viewer", "admin", "viewer", readToolNames},
		{"operator capped to viewer", "operator", "viewer", readToolNames},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemon, client := newFakeDaemon(t, tc.scope)
			s := startSession(t, &Server{API: client, MaxScope: tc.maxScope})
			assert.Equal(t, tc.want, s.toolNames())
			if len(tc.want) == len(readToolNames) {
				text, isErr := s.callTool("dispatch", map[string]any{"identifier": "ENG-1"})
				assert.True(t, isErr)
				assert.Contains(t, text, "needs an operator token")
				text, isErr = s.callTool("remember", map[string]any{"text": "x"})
				assert.True(t, isErr, text)
				assert.Empty(t, daemon.writes(), "a refused write tool never reaches the daemon")
			}
		})
	}
	assert.Equal(t, readToolNames, ToolNames("viewer"))
}

// TestMCPDispatchRespectsPause (#216): while all dispatch is paused,
// dispatch refuses with the pause's reason and changes nothing (no state
// move, no resume); once dispatch runs it moves the issue to the first
// active state.
func TestMCPDispatchRespectsPause(t *testing.T) {
	daemon, client := newFakeDaemon(t, "operator")
	daemon.state["dispatchPaused"] = map[string]any{"reason": "release freeze", "pausedAt": "2026-10-22T09:00:00Z"}
	s := startSession(t, &Server{API: client})

	text, isErr := s.callTool("dispatch", map[string]any{"identifier": "ENG-12"})
	require.True(t, isErr, text)
	assert.Contains(t, text, "all dispatch is paused (release freeze)")
	assert.Contains(t, text, "ENG-12 was not dispatched")
	assert.Empty(t, daemon.writes(), "no state move and no resume while paused")

	daemon.mu.Lock()
	daemon.state["dispatchPaused"] = nil
	daemon.mu.Unlock()
	text, isErr = s.callTool("dispatch", map[string]any{"identifier": "ENG-12"})
	require.False(t, isErr, text)
	assert.Equal(t, []string{"PATCH /issues/ENG-12/state"}, daemon.writes())
	assert.Equal(t, "Todo", daemon.lastBody("PATCH", "/issues/ENG-12/state")["state"])
}

// TestMCPReplyInputRefusesGuardrailApprovals (#216): an issue whose
// input-required entry is a guardrail approval (pendingApproval, the
// marker the dashboard reads) is refused without a request; an ordinary
// question is answered with fromAgent set, so an approval raised after the
// check is still refused by the event loop.
func TestMCPReplyInputRefusesGuardrailApprovals(t *testing.T) {
	daemon, client := newFakeDaemon(t, "operator")
	daemon.contexts["ENG-7"] = map[string]any{"identifier": "ENG-7", "run": map[string]any{
		"runId": "r1", "live": true, "pendingInput": "Agent wants to run: `git push --force`", "pendingApproval": true,
	}}
	daemon.contexts["ENG-8"] = map[string]any{"identifier": "ENG-8", "run": map[string]any{
		"runId": "r2", "live": true, "pendingInput": "Which database should I use?",
	}}
	daemon.contexts["ENG-9"] = map[string]any{"identifier": "ENG-9", "run": map[string]any{"runId": "r3", "live": true}}
	s := startSession(t, &Server{API: client})

	text, isErr := s.callTool("reply_input", map[string]any{"identifier": "ENG-7", "message": "approve"})
	require.True(t, isErr)
	assert.Contains(t, text, "guardrail approval")
	assert.Contains(t, text, "human-only")
	assert.Empty(t, daemon.writes(), "a guardrail approval is never answered")

	text, isErr = s.callTool("reply_input", map[string]any{"identifier": "ENG-9", "message": "hi"})
	require.True(t, isErr)
	assert.Contains(t, text, "not waiting for input")
	text, isErr = s.callTool("reply_input", map[string]any{"identifier": "ENG-404", "message": "hi"})
	require.True(t, isErr)
	assert.Contains(t, text, "not waiting for input")
	assert.Empty(t, daemon.writes())

	text, isErr = s.callTool("reply_input", map[string]any{"identifier": "ENG-8", "message": "Use staging."})
	require.False(t, isErr, text)
	assert.Equal(t, []string{"POST /issues/ENG-8/provide-input"}, daemon.writes())
	body := daemon.lastBody("POST", "/issues/ENG-8/provide-input")
	assert.Equal(t, "Use staging.", body["message"])
	assert.Equal(t, true, body["fromAgent"])
}

// TestMCPRememberSendsProposedStatus: remember always asks for a proposed
// entry with mcp provenance and the configured lessons scope, and refuses
// a caller that tries to set the status.
func TestMCPRememberSendsProposedStatus(t *testing.T) {
	daemon, client := newFakeDaemon(t, "operator")
	s := startSession(t, &Server{API: client})
	text, isErr := s.callTool("remember", map[string]any{"text": "staging DB is read-only", "status": "active"})
	require.True(t, isErr)
	assert.Contains(t, text, "invalid arguments")
	assert.Empty(t, daemon.writes())

	text, isErr = s.callTool("remember", map[string]any{"text": "staging DB is read-only", "tags": []string{"db"}})
	require.False(t, isErr, text)
	body := daemon.lastBody("POST", "/memory")
	assert.Equal(t, "proposed", body["status"])
	assert.Equal(t, "private", body["scope"])
	assert.Equal(t, "mcp", body["source"].(map[string]any)["kind"])
}

// TestMCPSteerFindsTheRunningSession: steer sends to the issue's running
// itervox session and points a waiting one at reply_input.
func TestMCPSteerFindsTheRunningSession(t *testing.T) {
	daemon, client := newFakeDaemon(t, "operator")
	daemon.sessions = []map[string]any{
		{"id": "ext-1", "owner": "external", "identifier": "ENG-1", "status": "running", "steering": "live"},
		{"id": "sess-1", "owner": "itervox", "identifier": "ENG-1", "status": "running", "steering": "live"},
		{"id": "sess-2", "owner": "itervox", "identifier": "ENG-2", "status": "input_required"},
	}
	s := startSession(t, &Server{API: client})
	text, isErr := s.callTool("steer", map[string]any{"identifier": "ENG-1", "message": "Use the v2 API."})
	require.False(t, isErr, text)
	assert.Equal(t, []string{"POST /sessions/sess-1/message"}, daemon.writes())
	text, isErr = s.callTool("steer", map[string]any{"identifier": "ENG-2", "message": "hi"})
	require.True(t, isErr)
	assert.Contains(t, text, "use reply_input")
}

// TestMCPReadToolsAreCompactAndRedacted: read tools return compact JSON,
// redact secrets and never relay agent output (lastEvent).
func TestMCPReadToolsAreCompactAndRedacted(t *testing.T) {
	secret := "ghp_" + strings.Repeat("A", 36)
	daemon, client := newFakeDaemon(t, "viewer")
	daemon.state["running"] = []map[string]any{{"identifier": "ENG-1", "turnCount": 3, "tokens": 900, "lastEvent": "AGENT OUTPUT " + secret}}
	daemon.state["recentFailures"] = []map[string]any{{"kind": "tracker", "message": "auth failed with " + secret, "count": 1}}
	daemon.state["dependencyAudit"] = []map[string]any{{"identifier": "ENG-2", "issueState": "Todo", "status": "blocked",
		"unresolvedBlockers": []map[string]any{{"identifier": "ENG-1", "state": "In Progress"}}}}
	daemon.state["dependencyGraphEdges"] = []map[string]any{{"sourceIdentifier": "ENG-1", "targetIdentifier": "ENG-3",
		"origin": "inferred", "confidence": 0.8, "gating": true, "evidence": "shares the schema"}}
	s := startSession(t, &Server{API: client})

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{{"board_summary", nil}, {"recent_failures", nil}, {"explain_blockers", nil}} {
		text, isErr := s.callTool(tc.tool, tc.args)
		require.False(t, isErr, text)
		assert.True(t, json.Valid([]byte(text)), text)
		assert.NotContains(t, text, secret, tc.tool)
		assert.NotContains(t, text, "AGENT OUTPUT", tc.tool)
		assert.NotContains(t, text, "\n", "compact JSON")
	}
	text, _ := s.callTool("explain_blockers", map[string]any{"identifier": "ENG-3"})
	var out struct {
		Tracker  []any            `json:"trackerBlockers"`
		Inferred []map[string]any `json:"inferredBlockers"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &out))
	assert.Empty(t, out.Tracker)
	require.Len(t, out.Inferred, 1)
	assert.Equal(t, true, out.Inferred[0]["gatesDispatch"])
}

// TestMCPProtocolBasics: version negotiation, ping, notifications get no
// answer, unknown methods and tools, parse errors and batches.
func TestMCPProtocolBasics(t *testing.T) {
	_, client := newFakeDaemon(t, "viewer")
	s := startSession(t, &Server{API: client, Version: "v-test"})

	resp := s.request("initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t"}})
	res := resp["result"].(map[string]any)
	assert.Equal(t, "2025-03-26", res["protocolVersion"])
	assert.Equal(t, "itervox", res["serverInfo"].(map[string]any)["name"])
	s.notify("notifications/initialized")

	resp = s.request("initialize", map[string]any{"protocolVersion": "1999-01-01"})
	assert.Equal(t, ProtocolVersions[0], resp["result"].(map[string]any)["protocolVersion"])

	resp = s.request("ping", nil) // the notification above produced no line
	assert.Equal(t, map[string]any{}, resp["result"])

	resp = s.request("resources/list", nil)
	assert.EqualValues(t, codeMethodNotFound, resp["error"].(map[string]any)["code"])
	resp = s.request("tools/call", map[string]any{"name": "nope"})
	assert.EqualValues(t, codeInvalidParams, resp["error"].(map[string]any)["code"])

	_, err := io.WriteString(s.in, "{not json\n")
	require.NoError(t, err)
	assert.EqualValues(t, codeParseError, s.read()["error"].(map[string]any)["code"])
	_, err = io.WriteString(s.in, `[{"jsonrpc":"2.0","id":9,"method":"ping"}]`+"\n")
	require.NoError(t, err)
	assert.EqualValues(t, codeInvalidRequest, s.read()["error"].(map[string]any)["code"])
}

// TestMCPListsReadToolsWhenDaemonDown: with the daemon unreachable,
// tools/list still answers (read tools only, so a client connects) and a
// tool call reports the problem as a tool error.
func TestMCPListsReadToolsWhenDaemonDown(t *testing.T) {
	s := startSession(t, &Server{API: NewClient("http://127.0.0.1:1", "x")})
	assert.Equal(t, readToolNames, s.toolNames())
	text, isErr := s.callTool("board_summary", nil)
	assert.True(t, isErr)
	assert.Contains(t, text, "not reachable")
}
