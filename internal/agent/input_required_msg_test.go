package agent_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

// CORE-166 negative corpus: real error strings, taken verbatim from `strings`
// on the installed claude (2.1.283) and codex (0.157.0) binaries, that name a
// non-interactive session, an approval/sandbox policy or a permission denial.
// They are configuration errors — no reply to a tracker comment can fix them —
// so the error event must stay Failed (retried), never InputRequired.
var realCLIConfigErrors = []string{
	// claude
	"Raw mode is not supported on the stdin provided to Ink.",
	"Failed to set raw mode",
	"Not an interactive terminal, so the command was only displayed, not accepted.",
	"This session is non-interactive, so Claude cannot run the OAuth flow here.",
	"input request 'q1' needs an elicitation surface this non-interactive session has none of",
	"Cloud session s1 is waiting for input that nothing on this machine can give (a permission prompt, a question, or something else)",
	"This command requires approval",
	"The Linear connector requires approval for this call.",
	`MCP server "github" is pending approval — approve it via /mcp first`,
	"PreModelSwitch hook: confirmation required, and this session cannot ask",
	"confirmation required (run /model interactively to confirm)",
	"Permission denied",
	// codex
	"stdin is not a terminal",
	"no terminal is available for a confirmation prompt (stdin/stderr is not a TTY). Run in a supported terminal or unset TERM.",
	"MCP tool call requires approval, but approval policy is never",
	"`git push` requires approval by policy",
	"invalid `approval_policy` config override: bogus",
	"command execution approval is not supported in exec mode for thread `t1`",
	"request_user_input is not supported in exec mode for thread `t1`",
	"you cannot request additional permissions unless the approval policy is OnRequest",
	"No pending approval found for call_id: c1",
	"No pending user input found for sub_id: s1",
	"queued user input exceeds the maximum length of 1000",
	// Composed from real fragments: a pending-answer phrase inside a
	// non-interactive error. The config marker must win — nobody can answer.
	"Claude needs your input, but this session is non-interactive",
	"Waiting for user input: stdin is not a terminal",
}

// Phrases that state the agent is waiting on a human answer.
var pendingHumanAnswers = []string{
	"Human turn required",
	"Claude needs your input",
	"An MCP server needs your input",
	"I need your input on the target database",
	"waiting for your answer in the elicitation dialog",
	"Waiting for your input before continuing",
	"Awaiting your input",
	"Waiting for user input: pick a target database",
}

func claudeErrorLine(t *testing.T, msg string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": msg})
	require.NoError(t, err)
	return b
}

func codexFailedLine(t *testing.T, msg string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": msg}})
	require.NoError(t, err)
	return b
}

func TestInputRequiredMsg_RealCLIConfigErrorsStayFailed(t *testing.T) {
	for _, msg := range realCLIConfigErrors {
		ev, err := agent.ParseLine(claudeErrorLine(t, msg))
		require.NoError(t, err)
		assert.True(t, ev.IsError, msg)
		assert.False(t, ev.IsInputRequired, "claude: config error flagged input-required: %q", msg)

		ev, err = agent.ParseCodexLine(codexFailedLine(t, msg))
		require.NoError(t, err)
		assert.True(t, ev.IsError, msg)
		assert.False(t, ev.IsInputRequired, "codex: config error flagged input-required: %q", msg)
	}
}

func TestInputRequiredMsg_PendingHumanAnswerStillFlags(t *testing.T) {
	for _, msg := range pendingHumanAnswers {
		ev, err := agent.ParseLine(claudeErrorLine(t, msg))
		require.NoError(t, err)
		assert.True(t, ev.IsInputRequired, "claude: pending human answer not flagged: %q", msg)

		ev, err = agent.ParseCodexLine(codexFailedLine(t, msg))
		require.NoError(t, err)
		assert.True(t, ev.IsInputRequired, "codex: pending human answer not flagged: %q", msg)
	}
}
