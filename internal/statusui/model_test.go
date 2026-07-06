package statusui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/server"
)

// logLine builds a JSON log buffer line matching the format written by formatBufLine.
func logLine(level, msg string, fields map[string]string) string {
	e := domain.BufLogEntry{
		Level: level,
		Msg:   msg,
		Time:  "00:00:00",
	}
	for k, v := range fields {
		switch k {
		case "session_id":
			e.SessionID = v
		case "text":
			e.Text = v
		case "tool":
			e.Tool = v
		case "description":
			e.Description = v
		case "task":
			e.Task = v
		case "status":
			e.Status = v
		case "exit_code":
			e.ExitCode = v
		case "output_size":
			e.OutputSize = v
		case "url":
			e.URL = v
		case "summary":
			e.Summary = v
		}
	}
	b, _ := json.Marshal(e)
	return string(b)
}

// ---------------------------------------------------------------------------
// colorLine — both claude and codex prefixes must produce styled output
// ---------------------------------------------------------------------------

func TestColorLine_ClaudeText(t *testing.T) {
	line := logLine("INFO", "claude: text", map[string]string{"session_id": "s1", "text": "hello world"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "claude text should produce styled output")
	assert.Contains(t, out, "hello world")
}

func TestColorLine_CodexText(t *testing.T) {
	line := logLine("INFO", "codex: text", map[string]string{"session_id": "s1", "text": "codex says hello"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "codex text should produce styled output")
	assert.Contains(t, out, "codex says hello")
}

func TestColorLine_ClaudeAction(t *testing.T) {
	line := logLine("INFO", "claude: action", map[string]string{"session_id": "s1", "tool": "Bash", "description": "ls -la"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "claude action should produce styled output")
	assert.Contains(t, out, "Bash")
}

func TestColorLine_CodexAction(t *testing.T) {
	line := logLine("INFO", "codex: action", map[string]string{"session_id": "s1", "tool": "shell", "description": "echo hi"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "codex action should produce styled output")
	assert.Contains(t, out, "shell")
}

func TestColorLine_ClaudeSubagent(t *testing.T) {
	line := logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "Investigate bug"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "claude subagent should produce styled output")
	assert.Contains(t, out, "Investigate bug")
}

func TestColorLine_CodexSubagent(t *testing.T) {
	line := logLine("INFO", "codex: subagent", map[string]string{"session_id": "s1", "tool": "spawn_agent", "description": "Fix the failing test"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "codex subagent should produce styled output")
	assert.Contains(t, out, "Fix the failing test")
}

func TestColorLine_ClaudeTodo(t *testing.T) {
	line := logLine("INFO", "claude: todo", map[string]string{"session_id": "s1", "task": "Write tests"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "claude todo should produce styled output")
	assert.Contains(t, out, "Write tests")
}

func TestColorLine_CodexTodo(t *testing.T) {
	line := logLine("INFO", "codex: todo", map[string]string{"session_id": "s1", "task": "Add documentation"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "codex todo should produce styled output")
	assert.Contains(t, out, "Add documentation")
}

func TestColorLine_CodexActionStarted(t *testing.T) {
	line := logLine("INFO", "codex: action_started", map[string]string{"session_id": "s1", "tool": "shell", "description": "long running build"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "codex action_started should produce styled output")
	assert.Contains(t, out, "shell")
}

func TestColorLine_ClaudeActionStarted(t *testing.T) {
	line := logLine("INFO", "claude: action_started", map[string]string{"session_id": "s1", "tool": "Bash", "description": "pnpm install"})
	out := colorLine(line)
	assert.NotEmpty(t, out, "claude action_started should produce styled output")
	assert.Contains(t, out, "Bash")
}

func TestColorLine_LifecycleEventsAreSkipped(t *testing.T) {
	// These lines appear in the buffer but should render as empty (suppressed from display).
	skipped := []string{
		logLine("INFO", "claude: session started", map[string]string{"session_id": "s1"}),
		logLine("INFO", "claude: turn done", map[string]string{"session_id": "s1"}),
		logLine("WARN", "claude: result error", map[string]string{"session_id": "s1"}),
		logLine("INFO", "codex: session started", map[string]string{"session_id": "s1"}),
		logLine("INFO", "codex: turn done", map[string]string{"session_id": "s1"}),
		logLine("WARN", "codex: result error", map[string]string{"session_id": "s1"}),
		logLine("INFO", "codex: action_detail", map[string]string{"session_id": "s1", "status": "completed", "exit_code": "0", "output_size": "42"}),
	}
	for _, line := range skipped {
		out := colorLine(line)
		assert.Empty(t, out, "lifecycle line should be suppressed: %q", line)
	}
}

// ---------------------------------------------------------------------------
// extractSubagents — already handles both backends but verify codex path
// ---------------------------------------------------------------------------

func TestExtractSubagents_CodexSubagentBoundaries(t *testing.T) {
	lines := []string{
		logLine("INFO", "codex: text", map[string]string{"session_id": "s1", "text": "starting"}),
		logLine("INFO", "codex: subagent", map[string]string{"session_id": "s1", "tool": "spawn_agent", "description": "Phase 1"}),
		logLine("INFO", "codex: action", map[string]string{"session_id": "s1", "tool": "shell", "description": "cmd1"}),
		logLine("INFO", "codex: subagent", map[string]string{"session_id": "s1", "tool": "spawn_agent", "description": "Phase 2"}),
		logLine("INFO", "codex: action", map[string]string{"session_id": "s1", "tool": "shell", "description": "cmd2"}),
	}
	subs := extractSubagents(lines)
	assert.Len(t, subs, 2)
	assert.Equal(t, "Phase 1", subs[0].description)
	assert.Equal(t, "Phase 2", subs[1].description)
}

func TestExtractSubagents_MixedBackends(t *testing.T) {
	lines := []string{
		logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "Claude sub"}),
		logLine("INFO", "codex: subagent", map[string]string{"session_id": "s1", "tool": "spawn_agent", "description": "Codex sub"}),
	}
	subs := extractSubagents(lines)
	assert.Len(t, subs, 2)

	names := make([]string, len(subs))
	for i, s := range subs {
		names[i] = s.description
	}
	assert.Contains(t, names, "Claude sub")
	assert.Contains(t, names, "Codex sub")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// Verify that colorLine produces different styled output for each event type
// so we can assert visual differentiation between them.
func TestColorLine_EventsAreVisuallyDifferent(t *testing.T) {
	textOut := colorLine(logLine("INFO", "codex: text", map[string]string{"session_id": "s1", "text": "hello"}))
	actionOut := colorLine(logLine("INFO", "codex: action", map[string]string{"session_id": "s1", "tool": "shell", "description": "ls"}))
	subagentOut := colorLine(logLine("INFO", "codex: subagent", map[string]string{"session_id": "s1", "tool": "spawn_agent", "description": "sub"}))
	startedOut := colorLine(logLine("INFO", "codex: action_started", map[string]string{"session_id": "s1", "tool": "shell", "description": "building"}))

	// They should all be non-empty and each one distinct from the others.
	for _, out := range []string{textOut, actionOut, subagentOut, startedOut} {
		assert.NotEmpty(t, out)
	}
	assert.NotEqual(t, textOut, actionOut)
	assert.NotEqual(t, actionOut, subagentOut)
	assert.NotEqual(t, actionOut, startedOut)
	// action and action_started share the tool name but differ in trailing "…"
	assert.True(t, strings.Contains(startedOut, "…"), "action_started should contain ellipsis")
}

// ---------------------------------------------------------------------------
// wrapText — word-wrapping pure function
// ---------------------------------------------------------------------------

func TestWrapText_BasicWrapping(t *testing.T) {
	// width=10: "hello"(5), "world foo"(9), "bar"(3)
	lines := wrapText("hello world foo bar", 10)
	assert.Equal(t, []string{"hello", "world foo", "bar"}, lines)
}

func TestWrapText_SingleWordFitsExactly(t *testing.T) {
	lines := wrapText("hello", 5)
	assert.Equal(t, []string{"hello"}, lines)
}

func TestWrapText_EmptyInput(t *testing.T) {
	lines := wrapText("", 20)
	assert.Empty(t, lines)
}

func TestWrapText_WhitespaceOnly(t *testing.T) {
	lines := wrapText("   ", 20)
	assert.Empty(t, lines)
}

func TestWrapText_ZeroWidth(t *testing.T) {
	// width <= 0 returns the whole string in a single element.
	lines := wrapText("hello world", 0)
	assert.Equal(t, []string{"hello world"}, lines)
}

func TestWrapText_NegativeWidth(t *testing.T) {
	lines := wrapText("hello world", -1)
	assert.Equal(t, []string{"hello world"}, lines)
}

func TestWrapText_LongWordExceedsWidth(t *testing.T) {
	// A word longer than width is never split — it appears as its own line.
	lines := wrapText("superlongword short", 5)
	assert.Equal(t, []string{"superlongword", "short"}, lines)
}

func TestWrapText_EachWordOnOwnLine(t *testing.T) {
	// width exactly equal to longest word — words share a line only when they fit together.
	lines := wrapText("one two three", 3)
	assert.Equal(t, []string{"one", "two", "three"}, lines)
}

// ---------------------------------------------------------------------------
// fmtDuration — seconds/minutes/hours formatting
// ---------------------------------------------------------------------------

func TestFmtDuration_Seconds(t *testing.T) {
	assert.Equal(t, "0s", fmtDuration(0))
	assert.Equal(t, "1s", fmtDuration(time.Second))
	assert.Equal(t, "59s", fmtDuration(59*time.Second))
}

func TestFmtDuration_Minutes(t *testing.T) {
	assert.Equal(t, "1m0s", fmtDuration(time.Minute))
	assert.Equal(t, "2m30s", fmtDuration(2*time.Minute+30*time.Second))
	assert.Equal(t, "59m59s", fmtDuration(59*time.Minute+59*time.Second))
}

func TestFmtDuration_Hours(t *testing.T) {
	assert.Equal(t, "1h0m0s", fmtDuration(time.Hour))
	assert.Equal(t, "1h30m15s", fmtDuration(time.Hour+30*time.Minute+15*time.Second))
}

func TestFmtDuration_Negative(t *testing.T) {
	// Negative durations are clamped to 0.
	assert.Equal(t, "0s", fmtDuration(-5*time.Second))
}

// ---------------------------------------------------------------------------
// fmtCount — token/number abbreviation
// ---------------------------------------------------------------------------

func TestFmtCount_SmallNumbers(t *testing.T) {
	assert.Equal(t, "0", fmtCount(0))
	assert.Equal(t, "999", fmtCount(999))
}

func TestFmtCount_Thousands(t *testing.T) {
	assert.Equal(t, "1.0k", fmtCount(1000))
	assert.Equal(t, "1.5k", fmtCount(1500))
	assert.Equal(t, "999.9k", fmtCount(999_900))
}

func TestFmtCount_Millions(t *testing.T) {
	assert.Equal(t, "1.0M", fmtCount(1_000_000))
	assert.Equal(t, "2.5M", fmtCount(2_500_000))
}

// ---------------------------------------------------------------------------
// truncate — ellipsis truncation
// ---------------------------------------------------------------------------

func TestTruncate_ShortString(t *testing.T) {
	assert.Equal(t, "hello", truncate("hello", 10))
}

func TestTruncate_ExactLength(t *testing.T) {
	assert.Equal(t, "hello", truncate("hello", 5))
}

func TestTruncate_TruncatesWithEllipsis(t *testing.T) {
	result := truncate("hello world", 8)
	assert.Equal(t, "hello w…", result)
	assert.Len(t, []rune(result), 8)
}

func TestTruncate_MaxOne(t *testing.T) {
	assert.Equal(t, "…", truncate("hello", 1))
}

func TestTruncate_MaxZero(t *testing.T) {
	// max <= 1 returns "…"
	assert.Equal(t, "…", truncate("hello", 0))
}

func TestTruncate_EmptyString(t *testing.T) {
	assert.Equal(t, "", truncate("", 10))
}

// ---------------------------------------------------------------------------
// extractPRLink — scans log lines for pr_opened url=
// ---------------------------------------------------------------------------

func TestExtractPRLink_Found(t *testing.T) {
	lines := []string{
		logLine("INFO", "claude: text", map[string]string{"session_id": "s1", "text": "working"}),
		logLine("INFO", "worker: pr_opened", map[string]string{"url": "https://github.com/org/repo/pull/42"}),
	}
	assert.Equal(t, "https://github.com/org/repo/pull/42", extractPRLink(lines))
}

func TestExtractPRLink_NotFound(t *testing.T) {
	lines := []string{
		logLine("INFO", "claude: text", map[string]string{"session_id": "s1", "text": "no PR here"}),
	}
	assert.Equal(t, "", extractPRLink(lines))
}

func TestExtractPRLink_Empty(t *testing.T) {
	assert.Equal(t, "", extractPRLink(nil))
	assert.Equal(t, "", extractPRLink([]string{}))
}

func TestExtractPRLink_ReturnsLastMatch(t *testing.T) {
	// extractPRLink scans from the end, so the last occurrence wins.
	lines := []string{
		logLine("INFO", "worker: pr_opened", map[string]string{"url": "https://github.com/org/repo/pull/1"}),
		logLine("INFO", "worker: pr_opened", map[string]string{"url": "https://github.com/org/repo/pull/2"}),
	}
	assert.Equal(t, "https://github.com/org/repo/pull/2", extractPRLink(lines))
}

// ---------------------------------------------------------------------------
// buildDurationAxis — timeline axis label placement
// ---------------------------------------------------------------------------

func TestBuildDurationAxis_ContainsZeroAndMax(t *testing.T) {
	axis := buildDurationAxis(0, 20, 60_000) // 0..60s, 20-char bar
	assert.Contains(t, axis, "0s")
	assert.Contains(t, axis, "30s") // midpoint
	assert.Contains(t, axis, "1m0s")
}

func TestBuildDurationAxis_WithOffset(t *testing.T) {
	axis := buildDurationAxis(5, 20, 10_000) // 5-char left offset
	assert.Contains(t, axis, "0s")
}

func TestBuildDurationAxis_SubSecond(t *testing.T) {
	// Very short elapsed — all three labels collapse to 0s/0s/0s but must not panic.
	axis := buildDurationAxis(0, 10, 0)
	assert.NotPanics(t, func() {
		_ = buildDurationAxis(0, 10, 0)
	})
	_ = axis
}

// ---------------------------------------------------------------------------
// buildNavItems — navigation list construction (issue rows + subagent rows)
// ---------------------------------------------------------------------------

func TestBuildNavItems_NoSessions(t *testing.T) {
	items := buildNavItems(nil, nil, nil)
	assert.Empty(t, items)
}

func TestBuildNavItems_SingleSessionNoSubagents(t *testing.T) {
	sessions := []server.RunningRow{{Identifier: "PROJ-1"}}
	items := buildNavItems(sessions, map[string][]subagentInfo{}, map[string]bool{})
	assert.Len(t, items, 1)
	assert.Equal(t, 0, items[0].issueIdx)
	assert.Equal(t, -1, items[0].subagentIdx, "issue header row has subagentIdx -1")
}

func TestBuildNavItems_SessionWithSubagents(t *testing.T) {
	sessions := []server.RunningRow{{Identifier: "PROJ-1"}}
	subs := map[string][]subagentInfo{
		"PROJ-1": {
			{description: "Phase 1", startLine: 0, endLine: 5},
			{description: "Phase 2", startLine: 5, endLine: 10},
		},
	}
	items := buildNavItems(sessions, subs, map[string]bool{})
	// Expect: 1 header + 2 subagent rows = 3
	assert.Len(t, items, 3)
	assert.Equal(t, -1, items[0].subagentIdx, "first row is the issue header")
	assert.Equal(t, 0, items[1].subagentIdx)
	assert.Equal(t, "Phase 1", items[1].label)
	assert.Equal(t, 1, items[2].subagentIdx)
	assert.Equal(t, "Phase 2", items[2].label)
}

func TestBuildNavItems_CollapsedSessionHidesSubagents(t *testing.T) {
	sessions := []server.RunningRow{{Identifier: "PROJ-1"}}
	subs := map[string][]subagentInfo{
		"PROJ-1": {{description: "Phase 1"}},
	}
	collapsed := map[string]bool{"PROJ-1": true}
	items := buildNavItems(sessions, subs, collapsed)
	// Collapsed: only the header row, no subagent rows.
	assert.Len(t, items, 1)
	assert.Equal(t, -1, items[0].subagentIdx)
}

func TestBuildNavItems_MultipleSessions(t *testing.T) {
	sessions := []server.RunningRow{
		{Identifier: "PROJ-1"},
		{Identifier: "PROJ-2"},
	}
	items := buildNavItems(sessions, map[string][]subagentInfo{}, map[string]bool{})
	assert.Len(t, items, 2)
	assert.Equal(t, 0, items[0].issueIdx)
	assert.Equal(t, 1, items[1].issueIdx)
}

// ---------------------------------------------------------------------------
// toolStyle — returns correct lipgloss style based on tool name category
// ---------------------------------------------------------------------------

func TestToolStyle_DoesNotPanic(t *testing.T) {
	// toolStyle is called during every TUI render; verify it handles all categories.
	names := []string{
		"bash", "shell", "execute", "sh", // amber — shell
		"read", "write", "edit", "glob", "ls", // green — file
		"webfetch", "fetch", "http", "navigate", // cyan — web
		"task", "agent", "dispatch", "subagent", // purple — AI orchestration
		"grep", "search", "find", // sky — search
		"unknown_tool", // muted — default
	}
	for _, name := range names {
		assert.NotPanics(t, func() {
			style := toolStyle(name)
			_ = style.Render("x")
		}, "toolStyle(%q) panicked", name)
	}
}

// ---------------------------------------------------------------------------
// isTodoState / isBacklogState — state classification helpers
// ---------------------------------------------------------------------------

func TestIsTodoState_Match(t *testing.T) {
	m := Model{cfg: Config{TodoStates: []string{"In Progress", "Review"}}}
	assert.True(t, m.isTodoState("In Progress"))
	assert.True(t, m.isTodoState("in progress"), "case-insensitive")
	assert.True(t, m.isTodoState("REVIEW"))
}

func TestIsTodoState_NoMatch(t *testing.T) {
	m := Model{cfg: Config{TodoStates: []string{"In Progress"}}}
	assert.False(t, m.isTodoState("Todo"))
	assert.False(t, m.isTodoState(""))
}

func TestIsTodoState_EmptyConfig(t *testing.T) {
	m := Model{cfg: Config{}}
	assert.False(t, m.isTodoState("In Progress"))
}

func TestIsBacklogState_Match(t *testing.T) {
	m := Model{cfg: Config{BacklogStates: []string{"Backlog", "Triage"}}}
	assert.True(t, m.isBacklogState("Backlog"))
	assert.True(t, m.isBacklogState("backlog"), "case-insensitive")
	assert.True(t, m.isBacklogState("TRIAGE"))
}

func TestIsBacklogState_NoMatch(t *testing.T) {
	m := Model{cfg: Config{BacklogStates: []string{"Backlog"}}}
	assert.False(t, m.isBacklogState("In Progress"))
	assert.False(t, m.isBacklogState(""))
}

func TestIsBacklogState_EmptyConfig(t *testing.T) {
	m := Model{cfg: Config{}}
	assert.False(t, m.isBacklogState("Backlog"))
}
