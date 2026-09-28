package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-164 — a vendor-signalled input request (a stream error event the
// parser flags IsInputRequired) must exit TerminalInputRequired, not
// TerminalFailed. These tests drive the REAL Claude/Codex runner against a
// fake agent binary (a shell script — no network, no real CLI), through the
// real worker and event loop, into a real outbox.

// stderrSecret is written to the fake agent's stderr. It deliberately matches
// none of the logging redaction patterns, so the only way to keep it out of
// the tracker question is to never build the question from stderr at all.
const stderrSecret = "SENTINEL_STDERR_SECRET_7c1e40"

const vendorInputQuestion = "Which database should I migrate: staging or prod?"

// fakeAgentScript writes an executable shell script named name (claude or
// codex) that writes an in-flight handoff file into its cwd (the workspace),
// prints a secret to stderr, emits stdout verbatim and exits 1 — the shape of
// a real CLI whose turn ended on an error result event.
func fakeAgentScript(t *testing.T, name, stdout string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"mkdir -p .itervox/handoff\n" +
		"printf '# in-flight deliverable\\n' > .itervox/handoff/20260926T000000Z_agent.md\n" +
		"echo 'export ANTHROPIC_API_KEY=" + stderrSecret + "' >&2\n" +
		"cat <<'ITERVOX_EOF'\n" + stdout + "\nITERVOX_EOF\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

type vendorRunHarness struct {
	orch  *orchestrator.Orchestrator
	ob    *outbox.Outbox
	mt    *tracker.MemoryTracker
	wsDir string
}

func startVendorRun(t *testing.T, runner agent.Runner, command string, mutate ...func(*config.Config)) (*vendorRunHarness, context.CancelFunc) {
	t.Helper()
	wsDir := t.TempDir()
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 10
	cfg.Agent.Command = command
	cfg.Agent.ReadTimeoutMs = 5000
	cfg.Tracker.FailedState = "Failed"
	cfg.Tracker.TerminalStates = append(cfg.Tracker.TerminalStates, "Failed")
	for _, m := range mutate {
		m(cfg)
	}

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates,
	)
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: wsDir})
	ob, err := outbox.New(filepath.Join(t.TempDir(), "outbox.json"))
	require.NoError(t, err)
	orch.SetWriteSink(orchestrator.NewOutboxWriteSink(ob))
	orch.SetOutbox(ob)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	go orch.Run(ctx) //nolint:errcheck
	return &vendorRunHarness{orch: orch, ob: ob, mt: mt, wsDir: wsDir}, cancel
}

func (h *vendorRunHarness) handoffNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(h.wsDir, ".itervox", "handoff"))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestVendorInputRequiredEventExitsInputRequired(t *testing.T) {
	claudeStream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-vendor-1"}`,
		`{"type":"assistant","session_id":"sess-vendor-1","message":{"content":[{"type":"text","text":"` + vendorInputQuestion + `"}],"usage":{"input_tokens":120,"output_tokens":30}}}`,
		`{"type":"result","subtype":"error","is_error":true,"session_id":"sess-vendor-1","result":"Human turn required"}`,
	}, "\n")
	claudeNoText := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-vendor-2"}`,
		`{"type":"result","subtype":"error","is_error":true,"session_id":"sess-vendor-2","result":"Waiting for user input: pick a target database"}`,
	}, "\n")
	codexStream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"th-vendor-3"}`,
		`{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"` + vendorInputQuestion + `"}}`,
		`{"type":"turn.failed","error":{"message":"Human turn required"},"usage":{"input_tokens":100,"output_tokens":20}}`,
	}, "\n")

	for _, tc := range []struct {
		name         string
		binary       string
		stream       string
		runner       agent.Runner
		wantQuestion string
	}{
		{"claude with agent-written question", "claude", claudeStream, agent.NewClaudeRunner(), vendorInputQuestion},
		{"claude vendor message only", "claude", claudeNoText, agent.NewClaudeRunner(), "Waiting for user input: pick a target database"},
		{"codex with agent-written question", "codex", codexStream, agent.NewCodexRunner(), vendorInputQuestion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cancel := startVendorRun(t, tc.runner, fakeAgentScript(t, tc.binary, tc.stream))
			defer cancel()

			var entry *orchestrator.InputRequiredEntry
			require.Eventually(t, func() bool {
				entry = h.orch.Snapshot().InputRequiredIssues["ENG-1"]
				return entry != nil
			}, 8*time.Second, 20*time.Millisecond,
				"a vendor input-required event must queue the issue for input, not fail it")
			cancel()

			snap := h.orch.Snapshot()
			assert.NotContains(t, snap.RetryAttempts, "id1", "an input request must not be retried")
			issues, err := h.mt.FetchIssueStatesByIDs(context.Background(), []string{"id1"})
			require.NoError(t, err)
			assert.Equal(t, "In Progress", issues[0].State, "an input request must not move the issue to failed_state")

			assert.Contains(t, entry.Context, tc.wantQuestion)
			assert.NotContains(t, entry.Context, stderrSecret, "stderr must never reach the input-required context")
			assert.NotContains(t, entry.Context, "stderr:", "the question must not carry the FailureText stderr segment")

			var question *outbox.Entry
			for _, e := range h.ob.Snapshot() {
				if e.Kind == outbox.KindCreateComment {
					question = &e
					break
				}
			}
			require.NotNil(t, question, "the input-required question must be queued in the outbox")
			assert.Contains(t, question.Body, tc.wantQuestion)
			assert.NotContains(t, question.Body, stderrSecret, "stderr must never reach the posted question")

			names := h.handoffNames(t)
			assert.Contains(t, names, "20260926T000000Z_agent.md",
				"TerminalInputRequired must leave the in-flight handoff un-renamed")
			for _, n := range names {
				assert.NotContains(t, n, ".partial.md", "input-required is a pause, not a failure")
			}
		})
	}
}

// Guard: a genuine error result with no input request still exits
// TerminalFailed — retried/failed, handoff marked partial, no question.
func TestVendorGenuineFailureStillExitsFailed(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-fail-1"}`,
		`{"type":"assistant","session_id":"sess-fail-1","message":{"content":[{"type":"text","text":"Running the migration."}],"usage":{"input_tokens":50,"output_tokens":10}}}`,
		`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-fail-1","result":"Tool execution crashed"}`,
	}, "\n")
	h, cancel := startVendorRun(t, agent.NewClaudeRunner(), fakeAgentScript(t, "claude", stream))
	defer cancel()

	require.Eventually(t, func() bool {
		for _, n := range h.handoffNames(t) {
			if strings.HasSuffix(n, ".partial.md") {
				return true
			}
		}
		return false
	}, 8*time.Second, 20*time.Millisecond, "a genuine failure must exit TerminalFailed and mark the handoff partial")
	cancel()

	assert.Nil(t, h.orch.Snapshot().InputRequiredIssues["ENG-1"], "a genuine failure must not be queued for input")
	for _, e := range h.ob.Snapshot() {
		assert.NotContains(t, e.Body, "Agent needs your input", "a genuine failure must not post a question")
	}
}

// The shared agenttest double now emits the real parser shape, so it drives
// the worker down the same TerminalInputRequired path (CORE-138/142).
func TestInputRequiredRunnerDoubleExitsInputRequired(t *testing.T) {
	h, cancel := startVendorRun(t, agenttest.InputRequiredRunner("sess-double", vendorInputQuestion), "claude")
	defer cancel()
	var entry *orchestrator.InputRequiredEntry
	require.Eventually(t, func() bool {
		entry = h.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return entry != nil
	}, 8*time.Second, 20*time.Millisecond)
	assert.Equal(t, vendorInputQuestion, entry.Context)
	assert.Equal(t, "sess-double", entry.SessionID)
}

// CORE-166: once CORE-164 made the InputRequired flag win over Failed, the
// parser's broad isInputRequiredMsg terms ("interactive", "approval", …) went
// live — a CLI configuration error turned into a wait for a human reply that
// can never help. Real error strings (from `strings` on the installed claude
// and codex binaries) must still exit TerminalFailed and be retried.
func TestVendorNonInteractiveCLIErrorIsRetriedNotInputRequired(t *testing.T) {
	for _, tc := range []struct {
		name, binary, stream string
		runner               agent.Runner
	}{
		{"claude non-interactive OAuth", "claude", strings.Join([]string{
			`{"type":"system","subtype":"init","session_id":"sess-cfg-1"}`,
			`{"type":"assistant","session_id":"sess-cfg-1","message":{"content":[{"type":"text","text":"Connecting the MCP server."}],"usage":{"input_tokens":40,"output_tokens":8}}}`,
			`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-cfg-1","result":"This session is non-interactive, so Claude cannot run the OAuth flow here."}`,
		}, "\n"), agent.NewClaudeRunner()},
		{"codex approval policy never", "codex", strings.Join([]string{
			`{"type":"thread.started","thread_id":"th-cfg-2"}`,
			`{"type":"turn.failed","error":{"message":"MCP tool call requires approval, but approval policy is never"},"usage":{"input_tokens":40,"output_tokens":8}}`,
		}, "\n"), agent.NewCodexRunner()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, cancel := startVendorRun(t, tc.runner, fakeAgentScript(t, tc.binary, tc.stream), func(cfg *config.Config) {
				cfg.Agent.MaxRetries = 5
				cfg.Agent.MaxRetryBackoffMs = 60_000 // the retry stays pending for the assertion
			})
			defer cancel()
			require.Eventually(t, func() bool {
				_, retrying := h.orch.Snapshot().RetryAttempts["id1"]
				return retrying
			}, 8*time.Second, 20*time.Millisecond, "a CLI configuration error must exit TerminalFailed and schedule a retry")
			cancel()
			assert.Nil(t, h.orch.Snapshot().InputRequiredIssues["ENG-1"], "a CLI configuration error must not wait for a human reply")
			for _, e := range h.ob.Snapshot() {
				assert.NotContains(t, e.Body, "Agent needs your input")
			}
		})
	}
}
