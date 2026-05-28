package orchestrator

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logbuffer"
)

func TestBuildSubAgentContext_UsesCodexToolName(t *testing.T) {
	ctx := buildSubAgentContext(map[string]config.AgentProfile{
		"research": {Prompt: "Investigate complex issues."},
	}, "", "codex")

	assert.Contains(t, ctx, "spawn_agent tool")
	assert.NotContains(t, ctx, "Task tool")
}

func TestBuildSubAgentContext_SkipsActiveProfile(t *testing.T) {
	ctx := buildSubAgentContext(map[string]config.AgentProfile{
		"active":    {Prompt: "Current worker"},
		"secondary": {Prompt: "Helper"},
	}, "active", "claude")

	assert.Contains(t, ctx, "secondary")
	assert.NotContains(t, ctx, "**active**")
}

func TestBuildSubAgentContext_UsesSoulSummaryInsteadOfInstructions(t *testing.T) {
	ctx := buildSubAgentContext(map[string]config.AgentProfile{
		"qa": {
			Soul:         "You are the QA specialist.\n\n## Purpose\nRun focused quality checks.",
			Instructions: "DO NOT INCLUDE: long operational checklist.",
		},
	}, "", "claude")

	assert.Contains(t, ctx, "You are the QA specialist.")
	assert.NotContains(t, ctx, "DO NOT INCLUDE")
	assert.NotContains(t, ctx, "Run focused quality checks.")
}

// --- formatBufLine / makeBufLine (JSON output) ---

func TestFormatBufLine_IncludesLevelAndMessage(t *testing.T) {
	line := formatBufLine("INFO", "hello world", nil)
	assert.Contains(t, line, `"level":"INFO"`, "got: %q", line)
	assert.Contains(t, line, `"msg":"hello world"`)
	assert.Contains(t, line, `"time":`)
}

func TestFormatBufLine_IncludesKeyValuePairs(t *testing.T) {
	line := formatBufLine("WARN", "something failed", []any{"tool", "Bash", "description", "ls"})
	assert.Contains(t, line, `"tool":"Bash"`)
	assert.Contains(t, line, `"description":"ls"`)
}

func TestFormatBufLine_TextFieldPopulated(t *testing.T) {
	line := formatBufLine("INFO", "claude: text", []any{"text", "has spaces"})
	assert.Contains(t, line, `"text":"has spaces"`)
}

func TestMakeBufLine_IncludesLevelAndTime(t *testing.T) {
	line := makeBufLine("INFO", "hello")
	assert.Contains(t, line, `"level":"INFO"`, "got: %q", line)
	assert.Contains(t, line, `"msg":"hello"`)
	assert.Contains(t, line, `"time":`)
}

// --- bufLogger.Debug / Warn ---

func TestBufLogger_Debug_DoesNotWriteToBuffer(t *testing.T) {
	buf := logbuffer.New()
	l := &bufLogger{base: slog.Default(), buf: buf, identifier: "ENG-1"}
	l.Debug("debug message", "key", "val")
	// Debug should not add to the buffer.
	assert.Nil(t, buf.Get("ENG-1"))
}

func TestBufLogger_Warn_WritesToBuffer(t *testing.T) {
	buf := logbuffer.New()
	l := &bufLogger{base: slog.Default(), buf: buf, identifier: "ENG-1"}
	l.Warn("something went wrong", "key", "val")
	lines := buf.Get("ENG-1")
	assert.Len(t, lines, 1)
	assert.Contains(t, lines[0], "WARN")
	assert.Contains(t, lines[0], "something went wrong")
}

func TestBufLogger_Info_WritesToBuffer(t *testing.T) {
	buf := logbuffer.New()
	l := &bufLogger{base: slog.Default(), buf: buf, identifier: "ENG-2"}
	l.Info("task dispatched", "issue", "ENG-2")
	lines := buf.Get("ENG-2")
	assert.Len(t, lines, 1)
	assert.Contains(t, lines[0], "INFO")
}
