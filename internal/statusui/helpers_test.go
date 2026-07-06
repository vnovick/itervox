package statusui

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/server"
)

// ---------------------------------------------------------------------------
// parseBufLine
// ---------------------------------------------------------------------------

func TestParseBufLine_ValidJSON(t *testing.T) {
	line := logLine("INFO", "claude: text", map[string]string{"session_id": "s1", "text": "hello"})
	entry, ok := parseBufLine(line)
	assert.True(t, ok)
	assert.Equal(t, "INFO", entry.Level)
	assert.Equal(t, "claude: text", entry.Msg)
	assert.Equal(t, "hello", entry.Text)
	assert.Equal(t, "s1", entry.SessionID)
}

func TestParseBufLine_InvalidJSON(t *testing.T) {
	entry, ok := parseBufLine("not json at all")
	assert.False(t, ok)
	assert.Equal(t, domain.BufLogEntry{}, entry)
}

func TestParseBufLine_EmptyString(t *testing.T) {
	entry, ok := parseBufLine("")
	assert.False(t, ok)
	assert.Equal(t, domain.BufLogEntry{}, entry)
}

func TestParseBufLine_EmptyObject(t *testing.T) {
	entry, ok := parseBufLine("{}")
	assert.True(t, ok)
	assert.Equal(t, "", entry.Level)
	assert.Equal(t, "", entry.Msg)
}

func TestParseBufLine_AllFields(t *testing.T) {
	line := logLine("WARN", "worker: pr_opened", map[string]string{
		"session_id":  "s2",
		"text":        "txt",
		"tool":        "Bash",
		"description": "desc",
		"task":        "task1",
		"status":      "completed",
		"exit_code":   "0",
		"output_size": "42",
		"url":         "https://example.com",
		"summary":     "sum",
	})
	entry, ok := parseBufLine(line)
	require.True(t, ok)
	assert.Equal(t, "WARN", entry.Level)
	assert.Equal(t, "worker: pr_opened", entry.Msg)
	assert.Equal(t, "s2", entry.SessionID)
	assert.Equal(t, "txt", entry.Text)
	assert.Equal(t, "Bash", entry.Tool)
	assert.Equal(t, "desc", entry.Description)
	assert.Equal(t, "task1", entry.Task)
	assert.Equal(t, "completed", entry.Status)
	assert.Equal(t, "0", entry.ExitCode)
	assert.Equal(t, "42", entry.OutputSize)
	assert.Equal(t, "https://example.com", entry.URL)
	assert.Equal(t, "sum", entry.Summary)
}

// ---------------------------------------------------------------------------
// termLine
// ---------------------------------------------------------------------------

func TestTermLine_ProducesPrefixAndMessage(t *testing.T) {
	out := termLine(">", "#00ff88", "hello world", "#e2e8f0")
	assert.Contains(t, out, ">")
	assert.Contains(t, out, "hello world")
}

func TestTermLine_EmptyPrefixAndMessage(t *testing.T) {
	out := termLine("", "#fff", "", "#fff")
	// Should still produce a space separator between empty styled strings.
	assert.NotEmpty(t, out)
}

func TestTermLine_ConsistentOutput(t *testing.T) {
	a := termLine(">", "#ff0000", "msg", "#00ff00")
	b := termLine(">", "#ff0000", "msg", "#00ff00")
	// Same inputs produce same output.
	assert.Equal(t, a, b)
}

// ---------------------------------------------------------------------------
// colorLine — additional edge cases
// ---------------------------------------------------------------------------

func TestColorLine_EmptyMessage(t *testing.T) {
	// A valid JSON line but with empty msg field.
	line := logLine("INFO", "", map[string]string{})
	out := colorLine(line)
	assert.Empty(t, out, "empty msg should produce empty output")
}

func TestColorLine_UnparseableLine(t *testing.T) {
	out := colorLine("this is not JSON")
	assert.Empty(t, out, "unparseable line should return empty string")
}

func TestColorLine_WorkerEvent(t *testing.T) {
	line := logLine("INFO", "worker: started", map[string]string{})
	out := colorLine(line)
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "worker: started")
}

func TestColorLine_WarnLevel(t *testing.T) {
	line := logLine("WARN", "something warning", map[string]string{})
	out := colorLine(line)
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "something warning")
}

func TestColorLine_ErrorLevel(t *testing.T) {
	line := logLine("ERROR", "something failed", map[string]string{})
	out := colorLine(line)
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "something failed")
}

func TestColorLine_DefaultNonEmptyMsg(t *testing.T) {
	line := logLine("INFO", "some random message", map[string]string{})
	out := colorLine(line)
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "some random message")
}

func TestColorLine_TextWithoutTextFieldIsEmpty(t *testing.T) {
	// "claude: text" but no text field → should return empty.
	line := logLine("INFO", "claude: text", map[string]string{"session_id": "s1"})
	out := colorLine(line)
	assert.Empty(t, out, "claude text without text field should return empty")
}

func TestColorLine_SubagentNoDescription(t *testing.T) {
	// Subagent with no description falls back to tool name.
	line := logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task"})
	out := colorLine(line)
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "Task")
}

func TestColorLine_ActionNoDescription(t *testing.T) {
	// Action with no description shows just the tool name.
	line := logLine("INFO", "claude: action", map[string]string{"session_id": "s1", "tool": "Bash"})
	out := colorLine(line)
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "Bash")
}

func TestColorLine_ActionNoToolNoDescription(t *testing.T) {
	// Action with neither tool nor description falls back to the msg.
	line := logLine("INFO", "claude: action", map[string]string{"session_id": "s1"})
	out := colorLine(line)
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "claude: action")
}

func TestColorLine_ActionStartedNoDescription(t *testing.T) {
	line := logLine("INFO", "codex: action_started", map[string]string{"session_id": "s1", "tool": "shell"})
	out := colorLine(line)
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "shell")
	assert.Contains(t, out, "…")
}

func TestColorLine_TodoWithoutTaskField(t *testing.T) {
	line := logLine("INFO", "claude: todo", map[string]string{"session_id": "s1"})
	out := colorLine(line)
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "claude: todo")
}

func TestColorLine_CodexActionDetail(t *testing.T) {
	// action_detail is always suppressed.
	line := logLine("INFO", "codex: action_detail", map[string]string{"session_id": "s1", "status": "completed"})
	out := colorLine(line)
	assert.Empty(t, out)
}

// ---------------------------------------------------------------------------
// extractSubagents — additional cases
// ---------------------------------------------------------------------------

func TestExtractSubagents_EmptyInput(t *testing.T) {
	subs := extractSubagents(nil)
	assert.Empty(t, subs)
}

func TestExtractSubagents_NoSubagentLines(t *testing.T) {
	lines := []string{
		logLine("INFO", "claude: text", map[string]string{"session_id": "s1", "text": "hello"}),
		logLine("INFO", "claude: action", map[string]string{"session_id": "s1", "tool": "Bash", "description": "ls"}),
	}
	subs := extractSubagents(lines)
	assert.Empty(t, subs)
}

func TestExtractSubagents_SingleSubagent(t *testing.T) {
	lines := []string{
		logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "Phase 1"}),
		logLine("INFO", "claude: action", map[string]string{"session_id": "s1", "tool": "Bash", "description": "ls"}),
	}
	subs := extractSubagents(lines)
	require.Len(t, subs, 1)
	assert.Equal(t, "Phase 1", subs[0].description)
	assert.Equal(t, 0, subs[0].startLine)
	assert.Equal(t, 2, subs[0].endLine, "endLine should equal len(lines) for last subagent")
}

func TestExtractSubagents_SubagentNoDescription(t *testing.T) {
	lines := []string{
		logLine("INFO", "codex: subagent", map[string]string{"session_id": "s1", "tool": "spawn_agent"}),
	}
	subs := extractSubagents(lines)
	require.Len(t, subs, 1)
	assert.Equal(t, "spawn_agent", subs[0].description, "falls back to tool name")
}

func TestExtractSubagents_UnparseableLineSkipped(t *testing.T) {
	lines := []string{
		"not valid json",
		logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "sub1"}),
	}
	subs := extractSubagents(lines)
	require.Len(t, subs, 1)
	assert.Equal(t, "sub1", subs[0].description)
	assert.Equal(t, 1, subs[0].startLine)
}

func TestExtractSubagents_ConsecutiveSubagents(t *testing.T) {
	lines := []string{
		logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "first"}),
		logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "second"}),
		logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "third"}),
	}
	subs := extractSubagents(lines)
	require.Len(t, subs, 3)
	// Verify boundaries.
	assert.Equal(t, 0, subs[0].startLine)
	assert.Equal(t, 1, subs[0].endLine)
	assert.Equal(t, 1, subs[1].startLine)
	assert.Equal(t, 2, subs[1].endLine)
	assert.Equal(t, 2, subs[2].startLine)
	assert.Equal(t, 3, subs[2].endLine) // len(lines)
}

// ---------------------------------------------------------------------------
// findNavItem
// ---------------------------------------------------------------------------

func TestFindNavItem_MatchesExact(t *testing.T) {
	sessions := []server.RunningRow{
		{Identifier: "A"},
		{Identifier: "B"},
	}
	navItems := []leftItem{
		{issueIdx: 0, subagentIdx: -1},
		{issueIdx: 0, subagentIdx: 0, label: "sub0"},
		{issueIdx: 1, subagentIdx: -1},
	}
	assert.Equal(t, 0, findNavItem(navItems, sessions, "A", -1))
	assert.Equal(t, 1, findNavItem(navItems, sessions, "A", 0))
	assert.Equal(t, 2, findNavItem(navItems, sessions, "B", -1))
}

func TestFindNavItem_NotFound(t *testing.T) {
	sessions := []server.RunningRow{{Identifier: "A"}}
	navItems := []leftItem{{issueIdx: 0, subagentIdx: -1}}
	assert.Equal(t, 0, findNavItem(navItems, sessions, "MISSING", -1))
}

func TestFindNavItem_EmptyInputs(t *testing.T) {
	assert.Equal(t, 0, findNavItem(nil, nil, "A", -1))
	assert.Equal(t, 0, findNavItem([]leftItem{}, []server.RunningRow{}, "A", -1))
}

func TestFindNavItem_IssueIdxOutOfRange(t *testing.T) {
	// navItem references an issueIdx beyond sessions length — should skip it.
	sessions := []server.RunningRow{{Identifier: "A"}}
	navItems := []leftItem{
		{issueIdx: 5, subagentIdx: -1}, // out of range
		{issueIdx: 0, subagentIdx: -1},
	}
	assert.Equal(t, 1, findNavItem(navItems, sessions, "A", -1))
}

// ---------------------------------------------------------------------------
// osc8Link
// ---------------------------------------------------------------------------

func TestOsc8Link_Format(t *testing.T) {
	link := osc8Link("https://example.com/pr/1", "PR #1")
	assert.Contains(t, link, "https://example.com/pr/1")
	assert.Contains(t, link, "PR #1")
	// Should start and end with OSC 8 sequences.
	assert.Contains(t, link, "\x1b]8;;")
	assert.Contains(t, link, "\x1b\\")
}

func TestOsc8Link_EmptyURL(t *testing.T) {
	link := osc8Link("", "text")
	assert.Contains(t, link, "text")
}

// ---------------------------------------------------------------------------
// pickerSlugAt
// ---------------------------------------------------------------------------

func TestPickerSlugAt_AllIssues(t *testing.T) {
	m := Model{}
	assert.Equal(t, "", m.pickerSlugAt(0))
}

func TestPickerSlugAt_NoProject(t *testing.T) {
	m := Model{}
	assert.Equal(t, "__no_project__", m.pickerSlugAt(1))
}

func TestPickerSlugAt_ProjectByIndex(t *testing.T) {
	m := Model{
		pickerProjects: []ProjectItem{
			{Slug: "alpha"},
			{Slug: "beta"},
		},
	}
	assert.Equal(t, "alpha", m.pickerSlugAt(2))
	assert.Equal(t, "beta", m.pickerSlugAt(3))
}

func TestPickerSlugAt_OutOfRange(t *testing.T) {
	m := Model{
		pickerProjects: []ProjectItem{{Slug: "alpha"}},
	}
	assert.Equal(t, "", m.pickerSlugAt(10))
}

// ---------------------------------------------------------------------------
// applyPickerFilter
// ---------------------------------------------------------------------------

func TestApplyPickerFilter_NilSetProjectFilter(t *testing.T) {
	m := Model{cfg: Config{SetProjectFilter: nil}}
	// Should not panic.
	assert.NotPanics(t, func() { m.applyPickerFilter() })
}

func TestApplyPickerFilter_AllSelected(t *testing.T) {
	var applied []string
	m := Model{
		cfg:       Config{SetProjectFilter: func(slugs []string) { applied = slugs }},
		pickerSel: map[string]bool{"": true, "alpha": true},
	}
	m.applyPickerFilter()
	assert.Equal(t, []string{}, applied, "all-issues selection clears filter")
}

func TestApplyPickerFilter_SpecificSlugs(t *testing.T) {
	var applied []string
	m := Model{
		cfg:       Config{SetProjectFilter: func(slugs []string) { applied = slugs }},
		pickerSel: map[string]bool{"alpha": true, "beta": false, "gamma": true},
	}
	m.applyPickerFilter()
	assert.Len(t, applied, 2)
	assert.Contains(t, applied, "alpha")
	assert.Contains(t, applied, "gamma")
}

func TestApplyPickerFilter_NoSelection(t *testing.T) {
	var applied []string
	m := Model{
		cfg:       Config{SetProjectFilter: func(slugs []string) { applied = slugs }},
		pickerSel: map[string]bool{},
	}
	m.applyPickerFilter()
	assert.Empty(t, applied, "no selection sends empty slugs")
}

// ---------------------------------------------------------------------------
// selectedSessionID
// ---------------------------------------------------------------------------

func TestSelectedSessionID_EmptyModel(t *testing.T) {
	m := Model{}
	id, kind := m.selectedSessionID()
	assert.Equal(t, "", id)
	assert.Equal(t, "", kind)
}

func TestSelectedSessionID_RunningSession(t *testing.T) {
	m := Model{
		sessions: []server.RunningRow{{Identifier: "X-1"}},
		navItems: []leftItem{{issueIdx: 0, subagentIdx: -1}},
	}
	id, kind := m.selectedSessionID()
	assert.Equal(t, "X-1", id)
	assert.Equal(t, "running", kind)
}

func TestSelectedSessionID_PausedSession(t *testing.T) {
	m := Model{
		inPausedSection: true,
		paused:          []string{"P-1", "P-2"},
		pausedCursor:    1,
	}
	id, kind := m.selectedSessionID()
	assert.Equal(t, "P-2", id)
	assert.Equal(t, "paused", kind)
}

func TestSelectedSessionID_HistoryTab(t *testing.T) {
	m := Model{
		leftTab: "history",
		history: []server.HistoryRow{
			{Identifier: "H-1"},
			{Identifier: "H-2"},
			{Identifier: "H-3"},
		},
		historyCursor: 0, // most recent = H-3
	}
	id, kind := m.selectedSessionID()
	assert.Equal(t, "H-3", id)
	assert.Equal(t, "history", kind)
}

func TestSelectedSessionID_HistoryCursorOutOfRange(t *testing.T) {
	m := Model{
		leftTab:       "history",
		history:       []server.HistoryRow{},
		historyCursor: 5,
	}
	id, kind := m.selectedSessionID()
	assert.Equal(t, "", id)
	assert.Equal(t, "", kind)
}

// ---------------------------------------------------------------------------
// ganttVisible
// ---------------------------------------------------------------------------

func TestGanttVisible_NoSessions(t *testing.T) {
	m := Model{}
	assert.False(t, m.ganttVisible())
}

func TestGanttVisible_WithSessions(t *testing.T) {
	m := Model{sessions: []server.RunningRow{{Identifier: "A"}}}
	assert.True(t, m.ganttVisible())
}

func TestGanttVisible_WithHistory(t *testing.T) {
	m := Model{history: []server.HistoryRow{{Identifier: "A"}}}
	assert.True(t, m.ganttVisible())
}

func TestGanttVisible_HiddenWhenBacklogOpen(t *testing.T) {
	m := Model{
		sessions:    []server.RunningRow{{Identifier: "A"}},
		backlogOpen: true,
	}
	assert.False(t, m.ganttVisible())
}

func TestGanttVisible_HiddenWhenBacklogLoading(t *testing.T) {
	m := Model{
		sessions:       []server.RunningRow{{Identifier: "A"}},
		backlogLoading: true,
	}
	assert.False(t, m.ganttVisible())
}

// ---------------------------------------------------------------------------
// computeGanttEntryCount
// ---------------------------------------------------------------------------

func TestComputeGanttEntryCount_NoSessions(t *testing.T) {
	m := Model{}
	assert.Equal(t, 0, m.computeGanttEntryCount())
}

func TestComputeGanttEntryCount_SessionsOnly(t *testing.T) {
	m := Model{
		sessions: []server.RunningRow{
			{Identifier: "A"},
			{Identifier: "B"},
		},
	}
	assert.Equal(t, 2, m.computeGanttEntryCount())
}

func TestComputeGanttEntryCount_SessionsPlusPaused(t *testing.T) {
	m := Model{
		sessions: []server.RunningRow{{Identifier: "A"}},
		paused:   []string{"B", "C"},
	}
	// A from sessions + B, C from paused (not in sessions) = 3
	assert.Equal(t, 3, m.computeGanttEntryCount())
}

func TestComputeGanttEntryCount_PausedOverlapWithSessions(t *testing.T) {
	m := Model{
		sessions: []server.RunningRow{{Identifier: "A"}},
		paused:   []string{"A"}, // overlaps
	}
	assert.Equal(t, 1, m.computeGanttEntryCount(), "overlapping paused item not double-counted")
}

func TestComputeGanttEntryCount_CappedAtMaxGanttBars(t *testing.T) {
	m := Model{
		sessions: []server.RunningRow{
			{Identifier: "A"},
			{Identifier: "B"},
			{Identifier: "C"},
			{Identifier: "D"},
			{Identifier: "E"},
			{Identifier: "F"},
		},
	}
	assert.Equal(t, maxGanttBars, m.computeGanttEntryCount())
}

func TestComputeGanttEntryCount_HistoryTab(t *testing.T) {
	m := Model{
		leftTab: "history",
		history: []server.HistoryRow{
			{Identifier: "A"},
			{Identifier: "A"},
			{Identifier: "B"},
		},
		historyCursor: 0, // selects last entry = "B"
	}
	// Only entries matching selectedSessionID ("B") are counted.
	assert.Equal(t, 1, m.computeGanttEntryCount())
}

// ---------------------------------------------------------------------------
// headerLineCount
// ---------------------------------------------------------------------------

func TestHeaderLineCount_Minimal(t *testing.T) {
	m := Model{
		snap: newTestSnap(server.StateSnapshot{}),
		cfg:  Config{},
	}
	assert.Equal(t, 3, m.headerLineCount(), "base header is 3 lines")
}

func TestHeaderLineCount_WithDashboardURL(t *testing.T) {
	m := Model{
		snap: newTestSnap(server.StateSnapshot{}),
		cfg:  Config{DashboardURL: "http://localhost:8090"},
	}
	assert.Equal(t, 4, m.headerLineCount())
}

func TestHeaderLineCount_WithRateLimits(t *testing.T) {
	m := Model{
		snap: newTestSnap(server.StateSnapshot{
			RateLimits: &server.RateLimitInfo{},
		}),
		cfg: Config{},
	}
	assert.Equal(t, 4, m.headerLineCount())
}

func TestHeaderLineCount_WithKillMsg(t *testing.T) {
	m := Model{
		snap:    newTestSnap(server.StateSnapshot{}),
		cfg:     Config{},
		killMsg: "pausing PROJ-1",
	}
	assert.Equal(t, 4, m.headerLineCount())
}

func TestHeaderLineCount_WithDispatchMsg(t *testing.T) {
	m := Model{
		snap:        newTestSnap(server.StateSnapshot{}),
		cfg:         Config{},
		dispatchMsg: "dispatched PROJ-2",
	}
	assert.Equal(t, 4, m.headerLineCount())
}

func TestHeaderLineCount_AllExtras(t *testing.T) {
	m := Model{
		snap: newTestSnap(server.StateSnapshot{
			RateLimits: &server.RateLimitInfo{},
		}),
		cfg:     Config{DashboardURL: "http://localhost:8090"},
		killMsg: "pausing PROJ-1",
	}
	// 3 base + 1 rate limit + 1 dashboard URL + 1 kill msg = 6
	assert.Equal(t, 6, m.headerLineCount())
}

func TestHeaderLineCount_WithInputRows(t *testing.T) {
	m := Model{
		snap: newTestSnap(server.StateSnapshot{
			InputRequired: []server.InputRequiredRow{
				{Identifier: "PROJ-1", State: "input_required"},
			},
		}),
		cfg: Config{},
	}
	assert.Equal(t, 4, m.headerLineCount())
}

func TestInputStateCounts(t *testing.T) {
	waiting, pending := inputStateCounts([]server.InputRequiredRow{
		{Identifier: "PROJ-1", State: "input_required"},
		{Identifier: "PROJ-2", State: "pending_input_resume"},
		{Identifier: "PROJ-3", State: "input_required"},
	})
	assert.Equal(t, 2, waiting)
	assert.Equal(t, 1, pending)
}

// ---------------------------------------------------------------------------
// currentNavItem / stableKey
// ---------------------------------------------------------------------------

func TestCurrentNavItem_Empty(t *testing.T) {
	m := Model{}
	_, ok := m.currentNavItem()
	assert.False(t, ok)
}

func TestCurrentNavItem_Valid(t *testing.T) {
	m := Model{
		navItems:    []leftItem{{issueIdx: 0, subagentIdx: -1}},
		selectedNav: 0,
	}
	item, ok := m.currentNavItem()
	assert.True(t, ok)
	assert.Equal(t, 0, item.issueIdx)
	assert.Equal(t, -1, item.subagentIdx)
}

func TestCurrentNavItem_OutOfRange(t *testing.T) {
	m := Model{
		navItems:    []leftItem{{issueIdx: 0, subagentIdx: -1}},
		selectedNav: 5,
	}
	_, ok := m.currentNavItem()
	assert.False(t, ok)
}

func TestStableKey_Empty(t *testing.T) {
	m := Model{}
	id, subIdx := m.stableKey()
	assert.Equal(t, "", id)
	assert.Equal(t, -1, subIdx)
}

func TestStableKey_WithSession(t *testing.T) {
	m := Model{
		sessions:    []server.RunningRow{{Identifier: "PROJ-1"}},
		navItems:    []leftItem{{issueIdx: 0, subagentIdx: -1}},
		selectedNav: 0,
	}
	id, subIdx := m.stableKey()
	assert.Equal(t, "PROJ-1", id)
	assert.Equal(t, -1, subIdx)
}

func TestStableKey_SubagentSelected(t *testing.T) {
	m := Model{
		sessions: []server.RunningRow{{Identifier: "PROJ-1"}},
		navItems: []leftItem{
			{issueIdx: 0, subagentIdx: -1},
			{issueIdx: 0, subagentIdx: 2, label: "sub"},
		},
		selectedNav: 1,
	}
	id, subIdx := m.stableKey()
	assert.Equal(t, "PROJ-1", id)
	assert.Equal(t, 2, subIdx)
}

// ---------------------------------------------------------------------------
// extractPRLink — additional edge cases
// ---------------------------------------------------------------------------

func TestExtractPRLink_UnparseableLinesSkipped(t *testing.T) {
	lines := []string{
		"not json",
		logLine("INFO", "worker: pr_opened", map[string]string{"url": "https://github.com/org/repo/pull/5"}),
	}
	assert.Equal(t, "https://github.com/org/repo/pull/5", extractPRLink(lines))
}

func TestExtractPRLink_PROpenedWithoutURL(t *testing.T) {
	lines := []string{
		logLine("INFO", "worker: pr_opened", map[string]string{}),
	}
	assert.Equal(t, "", extractPRLink(lines), "pr_opened without url field returns empty")
}

// ---------------------------------------------------------------------------
// defaultKeys — verify no panic and returns populated bindings
// ---------------------------------------------------------------------------

func TestDefaultKeys_NoPanic(t *testing.T) {
	keys := defaultKeys()
	assert.NotEmpty(t, keys.Quit.Keys())
	assert.NotEmpty(t, keys.ListUp.Keys())
	assert.NotEmpty(t, keys.ListDown.Keys())
}

func TestDefaultKeys_ShortHelp(t *testing.T) {
	keys := defaultKeys()
	bindings := keys.ShortHelp()
	assert.NotEmpty(t, bindings, "ShortHelp should return at least one binding")
}

func TestDefaultKeys_FullHelp(t *testing.T) {
	keys := defaultKeys()
	groups := keys.FullHelp()
	assert.NotEmpty(t, groups, "FullHelp should return at least one group")
}

// ---------------------------------------------------------------------------
// Update() — direct key-msg driven tests (no teatest harness needed)
// ---------------------------------------------------------------------------

// updateKey sends a single key press through Update and returns the new Model.
func updateKey(m Model, k string) Model {
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	return newM.(Model)
}

// updateKeyType sends a special key type through Update and returns the new Model.
func updateKeyType(m Model, kt tea.KeyType) Model {
	newM, _ := m.Update(tea.KeyMsg{Type: kt})
	return newM.(Model)
}

// readyModel builds a Model that has received WindowSizeMsg + tick so it's fully ready.
func readyModel(snap func() server.StateSnapshot) Model {
	if snap == nil {
		snap = newTestSnap(server.StateSnapshot{})
	}
	buf := logbuffer.New()
	m := New(snap, buf, Config{MaxAgents: 5}, func(id string) bool { return true })
	// Apply window size to make it ready.
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = newM.(Model)
	// Send a tick to populate state.
	newM, _ = m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	return m
}

// ---------------------------------------------------------------------------
// Key navigation: up/down in left pane
// ---------------------------------------------------------------------------

func TestUpdate_LeftPaneNavigation(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{
			{Identifier: "A"},
			{Identifier: "B"},
			{Identifier: "C"},
		},
	})
	m := readyModel(snap)
	assert.Equal(t, 0, m.selectedNav)

	// Down key moves selectedNav forward.
	m = updateKeyType(m, tea.KeyDown)
	assert.Equal(t, 1, m.selectedNav)

	m = updateKeyType(m, tea.KeyDown)
	assert.Equal(t, 2, m.selectedNav)

	// Down at end should not go further (or enter paused section).
	m = updateKeyType(m, tea.KeyDown)
	// selectedNav should be clamped.
	assert.True(t, m.selectedNav <= 2 || m.inPausedSection)

	// Up key moves back.
	if m.inPausedSection {
		m.inPausedSection = false
		m.selectedNav = 2
	}
	m = updateKeyType(m, tea.KeyUp)
	assert.Equal(t, 1, m.selectedNav)
}

// ---------------------------------------------------------------------------
// Key: space (toggle expand/collapse)
// ---------------------------------------------------------------------------

func TestUpdate_ToggleCollapse(t *testing.T) {
	buf := logbuffer.New()
	buf.Add("A", logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "Sub 1"}))
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
	})
	m := New(snap, buf, Config{MaxAgents: 5}, func(id string) bool { return true })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = newM.(Model)
	newM, _ = m.Update(tickMsg(time.Now()))
	m = newM.(Model)

	// Should start with subagent rows visible (not collapsed).
	assert.Greater(t, len(m.navItems), 1, "subagent row should be present")

	// Press space to collapse.
	m = updateKey(m, " ")
	assert.True(t, m.collapsed["A"])
	assert.Len(t, m.navItems, 1, "collapsed: only header row")

	// Press space again to expand.
	m = updateKey(m, " ")
	assert.False(t, m.collapsed["A"])
}

// ---------------------------------------------------------------------------
// Key: x (pause/kill)
// ---------------------------------------------------------------------------

func TestUpdate_PauseKey(t *testing.T) {
	cancelled := ""
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "PROJ-1"}},
	})
	m := readyModel(snap)
	m.cancelFn = func(id string) bool {
		cancelled = id
		return true
	}

	m = updateKey(m, "x")
	assert.Equal(t, "PROJ-1", cancelled)
	assert.Contains(t, m.killMsg, "Paused PROJ-1")
}

func TestUpdate_PauseKey_NoCancelFn(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "PROJ-1"}},
	})
	m := readyModel(snap)
	m.cancelFn = nil

	m = updateKey(m, "x")
	assert.Contains(t, m.killMsg, "not configured")
}

func TestUpdate_PauseKey_NoSelection(t *testing.T) {
	m := readyModel(nil)
	m.cancelFn = func(id string) bool { return true }

	m = updateKey(m, "x")
	assert.Contains(t, m.killMsg, "Select a running issue")
}

func TestUpdate_PauseKey_InPausedSection(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
		Paused:  []string{"P-1"},
	})
	m := readyModel(snap)
	m.inPausedSection = true
	m.pausedCursor = 0

	m = updateKey(m, "x")
	assert.Contains(t, m.killMsg, "x pauses running")
}

// ---------------------------------------------------------------------------
// Key: + / - (workers adjust)
// ---------------------------------------------------------------------------

func TestUpdate_WorkersUpDown(t *testing.T) {
	adjustDelta := 0
	snap := newTestSnap(server.StateSnapshot{})
	m := readyModel(snap)
	m.cfg.AdjustWorkers = func(delta int) { adjustDelta = delta }
	m.cfg.MaxAgents = 3

	m = updateKey(m, "+")
	assert.Equal(t, 1, adjustDelta)
	assert.Equal(t, 4, m.cfg.MaxAgents)
	assert.Contains(t, m.killMsg, "Workers")

	m = updateKey(m, "-")
	assert.Equal(t, -1, adjustDelta)
	assert.Equal(t, 3, m.cfg.MaxAgents)
}

func TestUpdate_WorkersDown_MinClamp(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{})
	m := readyModel(snap)
	m.cfg.AdjustWorkers = func(delta int) {}
	m.cfg.MaxAgents = 1

	m = updateKey(m, "-")
	assert.Equal(t, 1, m.cfg.MaxAgents, "cannot go below 1")
}

func TestUpdate_WorkersUp_MaxClamp(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{})
	m := readyModel(snap)
	m.cfg.AdjustWorkers = func(delta int) {}
	m.cfg.MaxAgents = 50

	m = updateKey(m, "+")
	assert.Equal(t, 50, m.cfg.MaxAgents, "cannot go above 50")
}

// ---------------------------------------------------------------------------
// Key: r (resume)
// ---------------------------------------------------------------------------

func TestUpdate_ResumeKey(t *testing.T) {
	resumed := ""
	snap := newTestSnap(server.StateSnapshot{
		Paused: []string{"P-1"},
	})
	m := readyModel(snap)
	m.cfg.ResumeIssue = func(id string) bool {
		resumed = id
		return true
	}
	m.pausedCursor = 0

	m = updateKey(m, "r")
	assert.Equal(t, "P-1", resumed)
	assert.Contains(t, m.killMsg, "Resumed P-1")
}

func TestUpdate_ResumeKey_Failure(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Paused: []string{"P-1"},
	})
	m := readyModel(snap)
	m.cfg.ResumeIssue = func(id string) bool { return false }
	m.pausedCursor = 0

	m = updateKey(m, "r")
	assert.Contains(t, m.killMsg, "Could not resume")
}

// ---------------------------------------------------------------------------
// Key: D (terminate)
// ---------------------------------------------------------------------------

func TestUpdate_TerminateRunning(t *testing.T) {
	terminated := ""
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "R-1"}},
	})
	m := readyModel(snap)
	m.cfg.TerminateIssue = func(id string) bool {
		terminated = id
		return true
	}

	// 'D' key
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	m = newM.(Model)
	assert.Equal(t, "R-1", terminated)
	assert.Contains(t, m.killMsg, "Cancelled R-1")
}

func TestUpdate_TerminatePaused(t *testing.T) {
	terminated := ""
	snap := newTestSnap(server.StateSnapshot{
		Paused: []string{"P-1"},
	})
	m := readyModel(snap)
	m.cfg.TerminateIssue = func(id string) bool {
		terminated = id
		return true
	}
	m.inPausedSection = true
	m.pausedCursor = 0

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	m = newM.(Model)
	assert.Equal(t, "P-1", terminated)
	assert.Contains(t, m.killMsg, "Discarded P-1")
}

// ---------------------------------------------------------------------------
// Key: tab (panel navigation)
// ---------------------------------------------------------------------------

func TestUpdate_TabCyclesPanels(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
	})
	m := readyModel(snap)
	assert.Equal(t, 0, m.activePanel)

	// Tab to right (logs) pane.
	m = updateKeyType(m, tea.KeyTab)
	assert.Equal(t, 1, m.activePanel)

	// Tab to gantt (bottom) pane.
	m = updateKeyType(m, tea.KeyTab)
	assert.Equal(t, 2, m.activePanel)

	// Tab back to left pane.
	m = updateKeyType(m, tea.KeyTab)
	assert.Equal(t, 0, m.activePanel)
}

// ---------------------------------------------------------------------------
// Key: esc (escape from modes)
// ---------------------------------------------------------------------------

func TestUpdate_EscFromHistory(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		History: []server.HistoryRow{{Identifier: "H-1"}},
	})
	m := readyModel(snap)
	m.leftTab = "history"

	m = updateKeyType(m, tea.KeyEsc)
	assert.Equal(t, "", m.leftTab)
}

func TestUpdate_EscFromPausedSection(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Paused: []string{"P-1"},
	})
	m := readyModel(snap)
	m.inPausedSection = true

	m = updateKeyType(m, tea.KeyEsc)
	assert.False(t, m.inPausedSection)
}

func TestUpdate_EscFromNonLeftPanel(t *testing.T) {
	m := readyModel(nil)
	m.activePanel = 1

	m = updateKeyType(m, tea.KeyEsc)
	assert.Equal(t, 0, m.activePanel)
}

// ---------------------------------------------------------------------------
// Key: h (history tab toggle)
// ---------------------------------------------------------------------------

func TestUpdate_HistoryTabToggle(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		History: []server.HistoryRow{{Identifier: "H-1"}},
	})
	m := readyModel(snap)

	m = updateKey(m, "h")
	assert.Equal(t, "history", m.leftTab)

	m = updateKey(m, "h")
	assert.Equal(t, "", m.leftTab)
}

// ---------------------------------------------------------------------------
// Key: j / k (log page scroll)
// ---------------------------------------------------------------------------

func TestUpdate_LogPageScroll(t *testing.T) {
	m := readyModel(nil)
	// Just verify no panics -- the viewport starts at zero offset.
	m = updateKey(m, "j")
	updateKey(m, "k")
	// No assertion needed beyond no panic.
}

// ---------------------------------------------------------------------------
// Key: up/down in right pane (logs scroll)
// ---------------------------------------------------------------------------

func TestUpdate_RightPaneScroll(t *testing.T) {
	m := readyModel(nil)
	m.activePanel = 1

	m = updateKeyType(m, tea.KeyDown)
	m = updateKeyType(m, tea.KeyUp)
	// No panic.
}

// ---------------------------------------------------------------------------
// Key: up/down in gantt pane
// ---------------------------------------------------------------------------

func TestUpdate_GanttPaneNavigation(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{
			{Identifier: "A"},
			{Identifier: "B"},
		},
	})
	m := readyModel(snap)
	m.activePanel = 2

	m = updateKeyType(m, tea.KeyDown)
	assert.Equal(t, 1, m.timelineCursor)

	m = updateKeyType(m, tea.KeyUp)
	assert.Equal(t, 0, m.timelineCursor)
}

// ---------------------------------------------------------------------------
// Key: enter in timeline pane (drill down)
// ---------------------------------------------------------------------------

func TestUpdate_EnterDrillDown(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
	})
	m := readyModel(snap)
	m.activePanel = 2
	m.timelineDetail = false

	m = updateKeyType(m, tea.KeyEnter)
	assert.True(t, m.timelineDetail)
}

// ---------------------------------------------------------------------------
// Key: s (split pane toggle)
// ---------------------------------------------------------------------------

func TestUpdate_SplitToggle(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
	})
	m := readyModel(snap)
	assert.False(t, m.splitMode)

	m = updateKey(m, "s")
	assert.True(t, m.splitMode, "split mode should be enabled with width >= 120")

	m = updateKey(m, "s")
	assert.False(t, m.splitMode, "split mode should toggle off")
}

func TestUpdate_SplitToggle_TooNarrow(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
	})
	m := New(snap, logbuffer.New(), Config{MaxAgents: 5}, func(id string) bool { return true })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = newM.(Model)
	newM, _ = m.Update(tickMsg(time.Now()))
	m = newM.(Model)

	m = updateKey(m, "s")
	assert.False(t, m.splitMode, "split should not enable when width < 120")
}

// ---------------------------------------------------------------------------
// Backlog panel: b key + navigation + dispatch
// ---------------------------------------------------------------------------

func TestUpdate_BacklogToggle(t *testing.T) {
	fetched := false
	m := readyModel(nil)
	m.cfg.FetchBacklog = func() ([]BacklogIssueItem, error) {
		fetched = true
		return []BacklogIssueItem{{Identifier: "BL-1"}}, nil
	}

	// Press b to open backlog (triggers async load).
	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = newM.(Model)
	assert.True(t, m.backlogLoading || fetched)

	// Simulate the loaded message.
	if cmd != nil {
		msg := cmd()
		newM, _ = m.Update(msg)
		m = newM.(Model)
	}
	assert.True(t, m.backlogOpen)

	// Press esc to close backlog.
	m = updateKeyType(m, tea.KeyEsc)
	assert.False(t, m.backlogOpen)
}

func TestUpdate_BacklogNavigation(t *testing.T) {
	m := readyModel(nil)
	m.backlogOpen = true
	m.backlogItems = []BacklogIssueItem{
		{Identifier: "BL-1"},
		{Identifier: "BL-2"},
		{Identifier: "BL-3"},
	}
	m.backlogCursor = 0

	m = updateKeyType(m, tea.KeyDown)
	assert.Equal(t, 1, m.backlogCursor)

	m = updateKeyType(m, tea.KeyDown)
	assert.Equal(t, 2, m.backlogCursor)

	m = updateKeyType(m, tea.KeyUp)
	assert.Equal(t, 1, m.backlogCursor)
}

func TestUpdate_BacklogDispatch(t *testing.T) {
	dispatched := ""
	m := readyModel(nil)
	m.backlogOpen = true
	m.backlogItems = []BacklogIssueItem{
		{Identifier: "BL-1"},
		{Identifier: "BL-2"},
	}
	m.backlogCursor = 0
	m.cfg.DispatchIssue = func(id string) error {
		dispatched = id
		return nil
	}

	m = updateKeyType(m, tea.KeyEnter)
	assert.Equal(t, "BL-1", dispatched)
	assert.Contains(t, m.dispatchMsg, "BL-1")
	assert.Len(t, m.backlogItems, 1, "dispatched item removed from list")
}

// ---------------------------------------------------------------------------
// Project picker: p key + navigation
// ---------------------------------------------------------------------------

func TestUpdate_ProjectPickerFlow(t *testing.T) {
	m := readyModel(nil)
	m.cfg.FetchProjects = func() ([]ProjectItem, error) {
		return []ProjectItem{
			{ID: "p1", Name: "Alpha", Slug: "alpha"},
			{ID: "p2", Name: "Beta", Slug: "beta"},
		}, nil
	}
	applied := []string(nil)
	m.cfg.SetProjectFilter = func(slugs []string) { applied = slugs }

	// Press p to open picker.
	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	m = newM.(Model)

	// Execute the async command.
	if cmd != nil {
		msg := cmd()
		newM, _ = m.Update(msg)
		m = newM.(Model)
	}
	assert.True(t, m.pickerOpen)

	// Navigate down and toggle selection with space.
	m = updateKeyType(m, tea.KeyDown) // cursor to row 1
	m = updateKey(m, " ")             // toggle selection
	slug := m.pickerSlugAt(m.pickerCursor)
	assert.True(t, m.pickerSel[slug])

	// Press enter to apply.
	m = updateKeyType(m, tea.KeyEnter)
	assert.False(t, m.pickerOpen)
	assert.NotNil(t, applied)
}

func TestUpdate_OpenWebUICopiesURL(t *testing.T) {
	orig := copyURLToClipboardFn
	t.Cleanup(func() { copyURLToClipboardFn = orig })

	called := make(chan string, 1)
	copyURLToClipboardFn = func(url string) {
		called <- url
	}

	m := readyModel(nil)
	m.cfg.DashboardURL = "http://127.0.0.1:8090/"

	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w")})
	m = newM.(Model)

	assert.Nil(t, cmd)
	select {
	case got := <-called:
		assert.Equal(t, "http://127.0.0.1:8090/", got)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("copyURLToClipboardFn was not called")
	}
	assert.Contains(t, m.killMsg, "Web UI URL copied")
}

func TestCopyURLToClipboard_PrefersNativeClipboardOnDarwin(t *testing.T) {
	origGOOS := clipboardGOOS
	origNative := copyURLToNativeClipboardFn
	origOSC52 := writeOSC52ClipboardFn
	t.Cleanup(func() {
		clipboardGOOS = origGOOS
		copyURLToNativeClipboardFn = origNative
		writeOSC52ClipboardFn = origOSC52
	})

	clipboardGOOS = "darwin"
	nativeCalls := 0
	osc52Calls := 0
	copyURLToNativeClipboardFn = func(url string) error {
		nativeCalls++
		assert.Equal(t, "http://127.0.0.1:8090/", url)
		return nil
	}
	writeOSC52ClipboardFn = func(string) {
		osc52Calls++
	}

	copyURLToClipboard("http://127.0.0.1:8090/")

	assert.Equal(t, 1, nativeCalls)
	assert.Equal(t, 0, osc52Calls)
}

func TestCopyURLToClipboard_FallsBackToOSC52WhenNativeClipboardFails(t *testing.T) {
	origGOOS := clipboardGOOS
	origNative := copyURLToNativeClipboardFn
	origOSC52 := writeOSC52ClipboardFn
	t.Cleanup(func() {
		clipboardGOOS = origGOOS
		copyURLToNativeClipboardFn = origNative
		writeOSC52ClipboardFn = origOSC52
	})

	clipboardGOOS = "darwin"
	copyURLToNativeClipboardFn = func(string) error {
		return errors.New("pbcopy failed")
	}
	copied := ""
	writeOSC52ClipboardFn = func(url string) {
		copied = url
	}

	copyURLToClipboard("http://127.0.0.1:8090/")

	assert.Equal(t, "http://127.0.0.1:8090/", copied)
}

// ---------------------------------------------------------------------------
// Profile picker flow
// ---------------------------------------------------------------------------

func TestUpdate_ProfilePickerFlow(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running:           []server.RunningRow{{Identifier: "R-1"}},
		AvailableProfiles: []string{"frontend", "backend"},
		ProfileDefs: map[string]server.ProfileDef{
			"frontend": {Command: "claude", Backend: "claude"},
			"backend":  {Command: "codex", Backend: "codex"},
		},
	})
	assigned := ""
	m := readyModel(snap)
	m.cfg.SetIssueProfile = func(id, profile string) { assigned = profile }
	m.cfg.IssueProfiles = func() map[string]string { return map[string]string{} }

	// Press 'a' to open profile picker.
	m = updateKey(m, "a")
	assert.True(t, m.profilePickerOpen)

	// Cursor starts on "clear override" row (index=len(profiles)) when no profile assigned.
	// Navigate up to first profile, then confirm.
	m.profilePickerCursor = 0
	m = updateKeyType(m, tea.KeyEnter)
	assert.False(t, m.profilePickerOpen)
	assert.Equal(t, "frontend", assigned)
}

// ---------------------------------------------------------------------------
// Picker close with esc
// ---------------------------------------------------------------------------

func TestUpdate_PickerCloseWithEsc(t *testing.T) {
	m := readyModel(nil)
	m.pickerOpen = true
	m.pickerProjects = []ProjectItem{{Slug: "a"}}

	m = updateKeyType(m, tea.KeyEsc)
	assert.False(t, m.pickerOpen)
}

func TestUpdate_ProfilePickerCloseWithEsc(t *testing.T) {
	m := readyModel(nil)
	m.profilePickerOpen = true
	m.profilePickerItems = []string{"frontend"}

	m = updateKeyType(m, tea.KeyEsc)
	assert.False(t, m.profilePickerOpen)
}

// ---------------------------------------------------------------------------
// Down into paused section from nav list
// ---------------------------------------------------------------------------

func TestUpdate_DownIntoPausedSection(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
		Paused:  []string{"P-1"},
	})
	m := readyModel(snap)
	assert.Equal(t, 0, m.selectedNav)

	// We're at the end of navItems (only 1 item), press down to enter paused section.
	m = updateKeyType(m, tea.KeyDown)
	assert.True(t, m.inPausedSection)
	assert.Equal(t, 0, m.pausedCursor)
}

// ---------------------------------------------------------------------------
// Tick: verify subagents, lastText, navItems rebuilt
// ---------------------------------------------------------------------------

func TestUpdate_TickRebuildsSubagentsAndLastText(t *testing.T) {
	buf := logbuffer.New()
	buf.Add("X-1", logLine("INFO", "claude: subagent", map[string]string{"session_id": "s1", "tool": "Task", "description": "Sub 1"}))
	buf.Add("X-1", logLine("INFO", "claude: text", map[string]string{"session_id": "s1", "text": "thinking about it"}))

	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "X-1"}},
	})
	m := New(snap, buf, Config{MaxAgents: 5}, func(id string) bool { return true })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = newM.(Model)
	newM, _ = m.Update(tickMsg(time.Now()))
	m = newM.(Model)

	assert.Equal(t, "thinking about it", m.lastText["X-1"])
	assert.Len(t, m.subagents["X-1"], 1)
	assert.Equal(t, "Sub 1", m.subagents["X-1"][0].description)
}

// ---------------------------------------------------------------------------
// Tick: paused cursor bounds
// ---------------------------------------------------------------------------

func TestUpdate_TickClampsPausedCursor(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Paused: []string{"P-1"},
	})
	m := readyModel(snap)
	m.inPausedSection = true
	m.pausedCursor = 5 // out of bounds

	newM, _ := m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	assert.Equal(t, 0, m.pausedCursor)
}

func TestUpdate_TickPausedBecomesEmpty(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Paused: []string{},
	})
	m := readyModel(snap)
	m.inPausedSection = true

	newM, _ := m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	assert.False(t, m.inPausedSection)
}

// ---------------------------------------------------------------------------
// Tick: history cursor clamped
// ---------------------------------------------------------------------------

func TestUpdate_TickClampsHistoryCursor(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		History: []server.HistoryRow{{Identifier: "H-1"}},
	})
	m := readyModel(snap)
	m.leftTab = "history"
	m.historyCursor = 10

	newM, _ := m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	assert.Equal(t, 0, m.historyCursor)
}

func TestUpdate_TickHistoryBecomesEmpty(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		History: []server.HistoryRow{},
	})
	m := readyModel(snap)
	m.leftTab = "history"

	newM, _ := m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	assert.Equal(t, "", m.leftTab)
}

// ---------------------------------------------------------------------------
// Tick: timeline detail reset
// ---------------------------------------------------------------------------

func TestUpdate_TickResetsTimelineWhenEmpty(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{})
	m := readyModel(snap)
	m.timelineDetail = true

	newM, _ := m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	assert.False(t, m.timelineDetail)
}

// ---------------------------------------------------------------------------
// Tick: profile overrides synced
// ---------------------------------------------------------------------------

func TestUpdate_TickSyncsProfileOverrides(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{})
	m := readyModel(snap)
	m.cfg.IssueProfiles = func() map[string]string {
		return map[string]string{"A": "frontend"}
	}

	newM, _ := m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	assert.Equal(t, "frontend", m.profileOverrides["A"])
}

// ---------------------------------------------------------------------------
// WindowSizeMsg — split mode disabled if too narrow
// ---------------------------------------------------------------------------

func TestUpdate_WindowSizeDisablesSplitWhenNarrow(t *testing.T) {
	m := readyModel(nil)
	m.splitMode = true

	// Resize to narrow window.
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = newM.(Model)
	assert.False(t, m.splitMode, "split should be disabled when width < 120")
}

// ---------------------------------------------------------------------------
// View rendering — smoke tests for various states
// ---------------------------------------------------------------------------

func TestView_BacklogOpenRenders(t *testing.T) {
	m := readyModel(nil)
	m.backlogOpen = true
	m.backlogItems = []BacklogIssueItem{
		{Identifier: "BL-1", Title: "Fix tests", State: "Todo"},
		{Identifier: "BL-2", Title: "Add docs", State: "Backlog"},
	}
	m.cfg.TodoStates = []string{"Todo"}
	m.cfg.BacklogStates = []string{"Backlog"}

	out := m.View()
	assert.NotEmpty(t, out)
}

func TestView_ProfilePickerRenders(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running:           []server.RunningRow{{Identifier: "R-1"}},
		AvailableProfiles: []string{"frontend", "backend"},
		ProfileDefs: map[string]server.ProfileDef{
			"frontend": {Command: "claude", Backend: "claude"},
		},
	})
	m := readyModel(snap)
	m.profilePickerOpen = true
	m.profilePickerItems = []string{"frontend", "backend"}
	m.profilePickerTarget = "R-1"
	m.profilePickerCursor = 0

	out := m.View()
	assert.NotEmpty(t, out)
}

func TestView_PickerRenders(t *testing.T) {
	m := readyModel(nil)
	m.pickerOpen = true
	m.pickerProjects = []ProjectItem{
		{ID: "p1", Name: "Alpha", Slug: "alpha"},
	}
	m.pickerSel = map[string]bool{"alpha": true}

	out := m.View()
	assert.NotEmpty(t, out)
}

func TestView_HistoryTabRenders(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		History: []server.HistoryRow{
			{Identifier: "H-1", Status: "succeeded"},
		},
	})
	m := readyModel(snap)
	m.leftTab = "history"

	out := m.View()
	assert.NotEmpty(t, out)
}

func TestView_InputSectionRenders(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		InputRequired: []server.InputRequiredRow{
			{Identifier: "WAIT-1", State: "input_required"},
			{Identifier: "RESUME-1", State: "pending_input_resume"},
		},
	})
	m := readyModel(snap)

	out := m.View()
	assert.Contains(t, out, "INPUT")
	assert.Contains(t, out, "WAIT-1")
	assert.Contains(t, out, "RESUME-1")
	assert.Contains(t, out, "reply in tracker/dashboard")
	assert.Contains(t, out, "waiting for reply")
	assert.Contains(t, out, "reply received")
}

func TestView_PRInfoRowUsesCopyLabel(t *testing.T) {
	buf := logbuffer.New()
	line, err := json.Marshal(domain.BufLogEntry{
		Msg: "worker: pr_opened",
		URL: "https://github.com/org/repo/pull/42",
	})
	require.NoError(t, err)
	buf.Add("RUN-1", string(line))

	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "RUN-1", State: "In Progress"}},
	})
	m := readyModel(snap)
	m.buf = buf

	out := m.View()
	assert.Contains(t, out, "o:copy")
	assert.NotContains(t, out, "o:open")
}

func TestView_SplitModeRenders(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
	})
	m := New(snap, logbuffer.New(), Config{MaxAgents: 5}, func(id string) bool { return true })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	m = newM.(Model)
	newM, _ = m.Update(tickMsg(time.Now()))
	m = newM.(Model)

	// Enable split mode.
	m = updateKey(m, "s")
	assert.True(t, m.splitMode)

	out := m.View()
	assert.NotEmpty(t, out)
}

// ---------------------------------------------------------------------------
// New — default MaxAgents
// ---------------------------------------------------------------------------

func TestNew_DefaultMaxAgents(t *testing.T) {
	m := New(newTestSnap(server.StateSnapshot{}), logbuffer.New(), Config{}, func(id string) bool { return true })
	assert.Equal(t, 10, m.cfg.MaxAgents)
}

func TestNew_CustomMaxAgents(t *testing.T) {
	m := New(newTestSnap(server.StateSnapshot{}), logbuffer.New(), Config{MaxAgents: 20}, func(id string) bool { return true })
	assert.Equal(t, 20, m.cfg.MaxAgents)
}

// ---------------------------------------------------------------------------
// Backlog close via b key
// ---------------------------------------------------------------------------

func TestUpdate_BacklogCloseViaB(t *testing.T) {
	m := readyModel(nil)
	m.backlogOpen = true
	m.backlogItems = []BacklogIssueItem{{Identifier: "BL-1"}}
	m.cfg.FetchBacklog = func() ([]BacklogIssueItem, error) { return nil, nil }

	m = updateKey(m, "b")
	assert.False(t, m.backlogOpen)
}

// ---------------------------------------------------------------------------
// Profile picker in backlog: assign from backlog
// ---------------------------------------------------------------------------

func TestUpdate_ProfilePickerFromBacklog(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		AvailableProfiles: []string{"frontend"},
	})
	m := readyModel(snap)
	m.backlogOpen = true
	m.backlogItems = []BacklogIssueItem{{Identifier: "BL-1"}}
	m.backlogCursor = 0
	m.cfg.SetIssueProfile = func(id, profile string) {}

	// Press 'a' while in backlog to open profile picker.
	m = updateKey(m, "a")
	assert.True(t, m.profilePickerOpen)
	assert.Equal(t, "BL-1", m.profilePickerTarget)
}

// ---------------------------------------------------------------------------
// Quit key
// ---------------------------------------------------------------------------

func TestUpdate_QuitReturnsQuitCmd(t *testing.T) {
	m := readyModel(nil)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	// The cmd should be tea.Quit.
	assert.NotNil(t, cmd)
}

func TestUpdate_QuitInvokesQuitApp(t *testing.T) {
	m := readyModel(nil)
	called := false
	m.cfg.QuitApp = func() { called = true }

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})

	assert.NotNil(t, cmd)
	assert.True(t, called)
}

// ---------------------------------------------------------------------------
// Confirm key.Matches works for custom keys
// ---------------------------------------------------------------------------

func TestKeyMatches_TerminateKey(t *testing.T) {
	keys := defaultKeys()
	// 'D' should match the Terminate binding.
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")}, keys.Terminate))
}

func TestKeyMatches_SplitToggle(t *testing.T) {
	keys := defaultKeys()
	assert.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}, keys.SplitToggle))
}

// ---------------------------------------------------------------------------
// Paused section: up from first paused returns to nav list
// ---------------------------------------------------------------------------

func TestUpdate_PausedSectionUpReturnsToNavList(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
		Paused:  []string{"P-1"},
	})
	m := readyModel(snap)
	m.inPausedSection = true
	m.pausedCursor = 0

	m = updateKeyType(m, tea.KeyUp)
	assert.False(t, m.inPausedSection, "should return to nav list")
	assert.Equal(t, len(m.navItems)-1, m.selectedNav, "should land on last nav item")
}

// ---------------------------------------------------------------------------
// History tab: navigation
// ---------------------------------------------------------------------------

func TestUpdate_HistoryNavigation(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		History: []server.HistoryRow{
			{Identifier: "H-1"},
			{Identifier: "H-2"},
			{Identifier: "H-3"},
		},
	})
	m := readyModel(snap)
	m.leftTab = "history"
	m.historyCursor = 0

	m = updateKeyType(m, tea.KeyDown)
	assert.Equal(t, 1, m.historyCursor)

	m = updateKeyType(m, tea.KeyDown)
	assert.Equal(t, 2, m.historyCursor)

	// Should not go beyond the end.
	m = updateKeyType(m, tea.KeyDown)
	assert.Equal(t, 2, m.historyCursor)

	m = updateKeyType(m, tea.KeyUp)
	assert.Equal(t, 1, m.historyCursor)
}

// ---------------------------------------------------------------------------
// Timeline detail: esc exits detail mode
// ---------------------------------------------------------------------------

func TestUpdate_EscFromTimelineDetail(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
	})
	m := readyModel(snap)
	m.timelineDetail = true

	m = updateKeyType(m, tea.KeyEsc)
	assert.False(t, m.timelineDetail)
}

// ---------------------------------------------------------------------------
// Gantt panel 3 (split mode): up/down
// ---------------------------------------------------------------------------

func TestUpdate_GanttPanel3Navigation(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{
			{Identifier: "A"},
			{Identifier: "B"},
		},
	})
	m := New(snap, logbuffer.New(), Config{MaxAgents: 5}, func(id string) bool { return true })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	m = newM.(Model)
	newM, _ = m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	m = updateKey(m, "s") // enable split mode
	m.activePanel = 3     // gantt when split

	m = updateKeyType(m, tea.KeyDown)
	assert.Equal(t, 1, m.timelineCursor)

	m = updateKeyType(m, tea.KeyUp)
	assert.Equal(t, 0, m.timelineCursor)
}

// ---------------------------------------------------------------------------
// Split details pane: up/down scroll
// ---------------------------------------------------------------------------

func TestUpdate_SplitDetailScroll(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running: []server.RunningRow{{Identifier: "A"}},
	})
	m := New(snap, logbuffer.New(), Config{MaxAgents: 5}, func(id string) bool { return true })
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	m = newM.(Model)
	newM, _ = m.Update(tickMsg(time.Now()))
	m = newM.(Model)
	m = updateKey(m, "s") // enable split
	m.activePanel = 2     // in split mode panel 2 is details

	m = updateKeyType(m, tea.KeyDown)
	m = updateKeyType(m, tea.KeyUp)
	// No panic.
}

// ---------------------------------------------------------------------------
// Enter from paused section focuses right pane
// ---------------------------------------------------------------------------

func TestUpdate_EnterFromPausedFocusesRightPane(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Paused: []string{"P-1"},
	})
	m := readyModel(snap)
	m.inPausedSection = true
	m.pausedCursor = 0
	m.activePanel = 0

	m = updateKeyType(m, tea.KeyEnter)
	assert.Equal(t, 1, m.activePanel)
}

// ---------------------------------------------------------------------------
// Picker navigation: up clamp and down clamp
// ---------------------------------------------------------------------------

func TestUpdate_PickerNavigationBounds(t *testing.T) {
	m := readyModel(nil)
	m.pickerOpen = true
	m.pickerProjects = []ProjectItem{{Slug: "a"}, {Slug: "b"}}
	m.pickerCursor = 0

	// Up at top should stay at 0.
	m = updateKeyType(m, tea.KeyUp)
	assert.Equal(t, 0, m.pickerCursor)

	// Down to last row.
	for i := 0; i < 5; i++ {
		m = updateKeyType(m, tea.KeyDown)
	}
	assert.Equal(t, 3, m.pickerCursor) // 2 projects + 2 special = 4 rows, max index = 3
}

// ---------------------------------------------------------------------------
// Profile picker: clear override row
// ---------------------------------------------------------------------------

func TestUpdate_ProfilePickerClearOverride(t *testing.T) {
	snap := newTestSnap(server.StateSnapshot{
		Running:           []server.RunningRow{{Identifier: "R-1"}},
		AvailableProfiles: []string{"frontend"},
	})
	assignedProfile := "something"
	m := readyModel(snap)
	m.cfg.SetIssueProfile = func(id, profile string) { assignedProfile = profile }
	m.cfg.IssueProfiles = func() map[string]string { return map[string]string{"R-1": "frontend"} }

	// Open profile picker.
	m = updateKey(m, "a")
	assert.True(t, m.profilePickerOpen)

	// Navigate to "clear override" row (last item = len(profiles)).
	for i := 0; i <= len(m.profilePickerItems); i++ {
		m = updateKeyType(m, tea.KeyDown)
	}

	m = updateKeyType(m, tea.KeyEnter)
	assert.Equal(t, "", assignedProfile, "clearing override should pass empty string")
	assert.Contains(t, m.killMsg, "cleared")
}
