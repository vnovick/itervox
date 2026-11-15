package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// runContextFixture is an orchestrator (loop not running) with one running
// run "run-1" on ENG-1 and a reporter for it on a fake clock.
type runContextFixture struct {
	o     *Orchestrator
	state State
	rep   *runContextReporter
	clock time.Time
}

func newRunContextFixture(t *testing.T) *runContextFixture {
	t.Helper()
	cfg := automationBaseCfg()
	o := New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil, nil)
	state := NewState(cfg)
	state.Running["issue-1"] = &RunEntry{
		Issue: domain.Issue{ID: "issue-1", Identifier: "ENG-1"}, RunID: "run-1",
		ProfileName: "coder", Backend: "claude", StartedAt: time.Now().Add(-time.Minute),
		TurnCount: 2, InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
	}
	o.storeSnap(state)
	f := &runContextFixture{o: o, state: state, clock: time.Unix(1_700_000_000, 0)}
	ctx := context.WithValue(context.Background(), runHandleKey{}, &runHandle{id: "run-1"})
	f.rep = o.newRunContextReporter(ctx, "ENG-1", "main")
	require.NotNil(t, f.rep)
	f.rep.now = func() time.Time { return f.clock }
	return f
}

// feed parses stream lines with parse, folds them into partial the way
// readLines does, and reports progress after each one.
func (f *runContextFixture) feed(t *testing.T, partial agent.TurnResult, parse func([]byte) (agent.StreamEvent, error), lines ...string) agent.TurnResult {
	t.Helper()
	for _, l := range lines {
		ev, err := parse([]byte(l))
		if err != nil {
			continue // readLines skips unparsable / irrelevant lines too
		}
		partial = agent.ApplyEvent(partial, ev)
		f.rep.observe(partial, false)
	}
	return partial
}

// drain hands every queued event to the loop handler and reports how many
// there were and whether each left the snapshot unannounced.
func (f *runContextFixture) drain(t *testing.T) int {
	t.Helper()
	n := 0
	for {
		select {
		case ev := <-f.o.events:
			require.Equal(t, EventRunContext, ev.Type)
			f.o.eventLeftStateUnchanged = false
			f.state = f.o.handleEvent(context.Background(), f.state, ev)
			assert.True(t, f.o.eventLeftStateUnchanged, "a run-context update must not store/announce a whole snapshot")
			n++
		default:
			return n
		}
	}
}

func claudeToolLine(name string, input map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"type": "assistant", "session_id": "s1",
		"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": name, "input": input}}},
	})
	return string(b)
}

func claudeTextLine(text string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "assistant", "session_id": "s1",
		"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}},
	})
	return string(b)
}

// TestRunContextTracksLastToolCallRedacted (#215): the run context follows
// the stream the runners already parse — the last assistant step and the
// last tool call's name and target — redacted (also for a secret the
// target's bound would cut), debounced to one update per second per run
// plus an end-of-turn flush, and read back by identifier joined with the
// live run's profile, backend and counters.
func TestRunContextTracksLastToolCallRedacted(t *testing.T) {
	f := newRunContextFixture(t)
	ghToken := "ghp_" + strings.Repeat("A", 36)
	antKey := "sk-ant-" + strings.Repeat("b", 40)
	// The second token starts at byte 285, so the target's 300-byte bound
	// falls in its middle: redacting after the cut would leak its prefix.
	longCmd := "git push https://x-access-token:" + ghToken + "@github.com/o/r.git " + strings.Repeat("x", 192) + " " + ghToken

	var partial agent.TurnResult
	partial = f.feed(t, partial, agent.ParseLine,
		claudeTextLine("Planning the change.\n\nUsing key "+antKey+" to call the API"),
		claudeToolLine("Read", map[string]any{"file_path": "internal/server/changes.go"}),
	)
	assert.Equal(t, 1, f.drain(t), "the first update goes out at once; the next one waits out the interval")

	f.clock = f.clock.Add(1500 * time.Millisecond)
	partial = f.feed(t, partial, agent.ParseLine, claudeToolLine("Bash", map[string]any{"command": longCmd, "description": "push"}))
	assert.Equal(t, 1, f.drain(t), "after the interval the newest state is sent")

	f.clock = f.clock.Add(100 * time.Millisecond)
	partial = f.feed(t, partial, agent.ParseLine, claudeToolLine("Edit", map[string]any{"file_path": "web/src/App.tsx", "old_string": "a", "new_string": "b"}))
	assert.Equal(t, 0, f.drain(t), "debounced: at most one update per second per run")
	f.rep.endTurn(partial)
	assert.Equal(t, 1, f.drain(t), "the end of a turn flushes the pending update")

	v, ok := f.o.RunContext("ENG-1")
	require.True(t, ok)
	assert.True(t, v.Live)
	assert.Equal(t, "run-1", v.RunID)
	assert.Equal(t, "coder", v.ProfileName)
	assert.Equal(t, "claude", v.Backend)
	assert.Equal(t, 2, v.TurnCount)
	assert.Equal(t, 15, v.TotalTokens)
	assert.Equal(t, "main", v.BaseBranch)
	assert.Equal(t, "Edit", v.LastToolName)
	assert.Equal(t, "web/src/App.tsx", v.LastToolTarget)
	assert.Equal(t, 3, v.ToolCalls)
	assert.Equal(t, f.clock, v.LastToolAt)
	assert.Contains(t, v.CurrentStep, "Planning the change. Using key", "whitespace collapsed")
	assert.NotContains(t, v.CurrentStep, antKey)

	// The Bash call in between was redacted, including the cut-off token.
	f.state.RunContexts["run-1"] = RunContext{}
	var p2 agent.TurnResult
	f.clock = f.clock.Add(2 * time.Second)
	p2 = f.feed(t, p2, agent.ParseLine, claudeToolLine("Bash", map[string]any{"command": longCmd}))
	f.rep.endTurn(p2)
	f.drain(t)
	v, ok = f.o.RunContext("ENG-1")
	require.True(t, ok)
	assert.Equal(t, "Bash", v.LastToolName)
	assert.Contains(t, v.LastToolTarget, "git push https://x-access-token:")
	assert.NotContains(t, v.LastToolTarget, "ghp_AAAA", "no prefix of the token survives the cut")
	assert.LessOrEqual(t, len(v.LastToolTarget), runContextTargetMaxBytes)

	// The SSE frame is unaffected: the context lives only in RunContexts.
	snap := f.o.Snapshot()
	assert.Equal(t, "", snap.Running["issue-1"].LastMessage)
}

// TestRunContextNeverIncludesToolOutput (#215): neither a command's output
// (Codex command_execution aggregated_output), nor a tool result (Claude
// tool_result blocks), nor a tool's result/content fields reach the run
// context — live or after the run exits.
func TestRunContextNeverIncludesToolOutput(t *testing.T) {
	const marker = "TOOL-OUTPUT-MARKER-7f3a"
	f := newRunContextFixture(t)
	codexStarted, _ := json.Marshal(map[string]any{"type": "item.started", "item": map[string]any{
		"id": "i1", "type": "command_execution", "command": "go test ./...", "status": "in_progress"}})
	codexDone, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{
		"id": "i1", "type": "command_execution", "command": "go test ./...",
		"aggregated_output": marker + "\nok", "exit_code": 0, "status": "completed"}})
	codexCollab, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{
		"id": "i2", "type": "collab_tool_call", "tool": "wait", "status": "completed",
		"agents_states": map[string]any{"t1": map[string]any{"status": "done", "message": marker}}}})
	var partial agent.TurnResult
	partial = f.feed(t, partial, agent.ParseCodexLine, string(codexStarted), string(codexDone), string(codexCollab))
	f.rep.endTurn(partial)
	f.drain(t)
	afterCodex, ok := f.o.RunContext("ENG-1")
	require.True(t, ok)
	assert.Equal(t, "wait", afterCodex.LastToolName)

	claudeResult, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "t", "content": marker}}}})
	var p2 agent.TurnResult
	f.clock = f.clock.Add(2 * time.Second)
	p2 = f.feed(t, p2, agent.ParseLine,
		claudeToolLine("mcp__tracker__get_issue", map[string]any{"output": marker, "result": marker, "content": marker, "stdout": marker}),
		string(claudeResult),
	)
	f.rep.endTurn(p2)
	f.drain(t)
	afterMCP, ok := f.o.RunContext("ENG-1")
	require.True(t, ok)
	assert.Equal(t, "mcp__tracker__get_issue", afterMCP.LastToolName)

	var p3 agent.TurnResult
	p3 = f.feed(t, p3, agent.ParseLine,
		claudeToolLine("Bash", map[string]any{"command": "make verify", "output": marker, "stdout": marker, "stderr": marker}),
	)
	f.rep.endTurn(p3)
	f.drain(t)

	live, ok := f.o.RunContext("ENG-1")
	require.True(t, ok)
	assert.Equal(t, "Bash", live.LastToolName)
	assert.Equal(t, "make verify", live.LastToolTarget)
	assert.Equal(t, 4, live.ToolCalls, "completed calls only (item.started is not counted)")

	// The run exits: the finished context keeps the same, output-free view.
	exit := *f.state.Running["issue-1"]
	exit.TerminalReason = TerminalSucceeded
	finishRunContext(&f.state, f.state.Running["issue-1"], &exit, "run-1", time.Now())
	delete(f.state.Running, "issue-1")
	f.o.storeSnap(f.state)
	done, ok := f.o.RunContext("ENG-1")
	require.True(t, ok)
	assert.False(t, done.Live)
	assert.Equal(t, "succeeded", done.Outcome)
	assert.Equal(t, "coder", done.ProfileName)

	for _, v := range []RunContextView{afterCodex, afterMCP, live, done} {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		assert.NotContains(t, string(b), marker)
	}
}

// TestRunContextBoundedAndGuardrailsCounted (#215): finished contexts are
// trimmed to runContextFinishedCap, a stale run's update is ignored, and a
// guardrail refusal is counted on the running run.
func TestRunContextBoundedAndGuardrailsCounted(t *testing.T) {
	f := newRunContextFixture(t)
	base := time.Now()
	for i := range runContextFinishedCap + 5 {
		e := &RunEntry{Issue: domain.Issue{ID: fmt.Sprint(i), Identifier: fmt.Sprintf("OLD-%d", i)}, RunID: fmt.Sprintf("old-%d", i)}
		finishRunContext(&f.state, e, e, "", base.Add(time.Duration(i)*time.Second))
	}
	assert.Len(t, f.state.RunContexts, runContextFinishedCap)
	_, kept := f.state.RunContexts["old-0"]
	assert.False(t, kept, "the oldest finished context is dropped first")

	f.o.events <- OrchestratorEvent{Type: EventRunContext, RunID: "gone", runContext: &runContextUpdate{RunID: "gone", Identifier: "ENG-9", CurrentStep: "x"}}
	f.drain(t)
	_, stale := f.state.RunContexts["gone"]
	assert.False(t, stale, "an update for a run that is not running is ignored")

	require.True(t, f.o.RecordFailure(FailureRecord{Kind: FailureKindGuardrail, Identifier: "ENG-1", Source: "push_protected", Message: "refused"}))
	f.state = f.o.handleEvent(context.Background(), f.state, <-f.o.events)
	f.o.storeSnap(f.state)
	v, ok := f.o.RunContext("ENG-1")
	require.True(t, ok)
	assert.Equal(t, 1, v.GuardrailBlocks)
	assert.Equal(t, "push_protected", v.LastGuardrailKind)

	_, ok = f.o.RunContext("NOPE-1")
	assert.False(t, ok)
}
