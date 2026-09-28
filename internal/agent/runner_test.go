package agent_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
)

// CORE-144/CORE-145: these two tests used to drive agenttest.FakeRunner,
// which never builds any real argv — it just replays scripted StreamEvents —
// so they could not have caught a regression in buildDirectArgs's -p/--resume
// construction. They now drive the real agent.ClaudeRunner against a fake
// "claude" executable that dumps its argv, the same fake-binary idiom used
// throughout codex_test.go (e.g. TestCodexRunnerResumeTurn's argFile).

func TestRunTurnFirstTurnBuildsPromptFlag(t *testing.T) {
	fakeOutput := strings.Join([]string{
		`{"type":"system","session_id":"sess-1"}`,
		`{"type":"result","subtype":"success","session_id":"sess-1"}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeExe := filepath.Join(dir, "claude")
	argFile := filepath.Join(dir, "args.txt")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %[1]s\ncat >> %[1]s\nprintf '%%s' %[2]s\n", shellLiteral(argFile), shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	runner := agent.NewClaudeRunner()
	result, err := runner.RunTurn(context.Background(), slog.Default(), nil, nil, "do the thing", dir, fakeExe, "", "", 30000, 60000, agent.PermissionBypass)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", result.SessionID)
	assert.False(t, result.Failed)

	argv, err := os.ReadFile(argFile)
	require.NoError(t, err)
	// argv, then the stdin the fake copied (CORE-156: -p with no prompt
	// argument, the prompt on stdin).
	assert.Equal(t, "--output-format stream-json --verbose --dangerously-skip-permissions -p\ndo the thing", string(argv),
		"a first turn (nil sessionID) must build the -p flag and deliver the prompt on stdin")
	assert.NotContains(t, string(argv), "--resume", "a first turn must not pass --resume")
}

func TestRunTurnContinuationUsesResume(t *testing.T) {
	fakeOutput := strings.Join([]string{
		`{"type":"system","session_id":"sess-1"}`,
		`{"type":"result","subtype":"success","session_id":"sess-1"}`,
	}, "\n") + "\n"

	dir := t.TempDir()
	fakeExe := filepath.Join(dir, "claude")
	argFile := filepath.Join(dir, "args.txt")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" > %[1]s\ncat >> %[1]s\nprintf '%%s' %[2]s\n", shellLiteral(argFile), shellLiteral(fakeOutput))
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	runner := agent.NewClaudeRunner()
	sessionID := "sess-1"
	result, err := runner.RunTurn(context.Background(), slog.Default(), nil, &sessionID, "continue", dir, fakeExe, "", "", 30000, 60000, agent.PermissionBypass)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", result.SessionID)

	argv, err := os.ReadFile(argFile)
	require.NoError(t, err)
	assert.Contains(t, string(argv), "--resume sess-1", "a continuation turn must pass --resume with the session ID")
	assert.True(t, strings.HasSuffix(string(argv), " -p\ncontinue"), "the reply must arrive on stdin after -p; got %q", argv)
}

// TestFakeRunnerRecordsSessionIDs gives agenttest.FakeRunner.SessionIDs a
// live read site (CORE-144): the field was recorded on every RunTurn call
// but read by no test anywhere in the repo. It exists specifically so
// orchestrator-level tests can verify which sessionID pointer value (as a
// string, "" for nil) each dispatched turn carried, across multiple calls to
// the same fake runner — verify that shape directly here.
func TestFakeRunnerRecordsSessionIDs(t *testing.T) {
	dir := t.TempDir()
	fake := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "sess-1"},
		{Type: "result", SessionID: "sess-1"},
	})

	_, err := fake.RunTurn(context.Background(), slog.Default(), nil, nil, "first", dir, "claude", "", "", 30000, 60000, agent.PermissionBypass)
	require.NoError(t, err)

	sid := "sess-1"
	_, err = fake.RunTurn(context.Background(), slog.Default(), nil, &sid, "continue", dir, "claude", "", "", 30000, 60000, agent.PermissionBypass)
	require.NoError(t, err)

	assert.Equal(t, []string{"", "sess-1"}, fake.SessionIDs, "SessionIDs must record \"\" for the nil-sessionID call and the resumed ID for the second")
}

func TestRunTurnFailedOnErrorResult(t *testing.T) {
	dir := t.TempDir()
	fake := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "sess-1"},
		{Type: "result", SessionID: "sess-1", IsError: true},
	})
	result, err := fake.RunTurn(context.Background(), slog.Default(), nil, nil, "prompt", dir, "claude", "", "", 30000, 60000, agent.PermissionBypass)
	require.NoError(t, err)
	assert.True(t, result.Failed)
}

func TestRunTurnInputRequiredSetsFlag(t *testing.T) {
	dir := t.TempDir()
	fake := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "sess-1"},
		{Type: "result", SessionID: "sess-1", IsError: true, IsInputRequired: true},
	})
	result, err := fake.RunTurn(context.Background(), slog.Default(), nil, nil, "prompt", dir, "claude", "", "", 30000, 60000, agent.PermissionBypass)
	require.NoError(t, err)
	assert.True(t, result.Failed)
	assert.True(t, result.InputRequired)
}

func TestRunTurnTokensAccumulated(t *testing.T) {
	dir := t.TempDir()
	fake := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "sess-1"},
		{Type: "assistant", Usage: agent.UsageSnapshot{InputTokens: 100, OutputTokens: 50}},
		{Type: "result", SessionID: "sess-1"},
	})
	result, err := fake.RunTurn(context.Background(), slog.Default(), nil, nil, "prompt", dir, "claude", "", "", 30000, 60000, agent.PermissionBypass)
	require.NoError(t, err)
	assert.Equal(t, 100, result.InputTokens)
	assert.Equal(t, 50, result.OutputTokens)
}

// ─── ApplyEvent unit tests ────────────────────────────────────────────────────

func TestApplyEventSystem(t *testing.T) {
	tests := []struct {
		name    string
		initial agent.TurnResult
		ev      agent.StreamEvent
		wantSID string
	}{
		{
			name:    "sets SessionID when empty",
			initial: agent.TurnResult{},
			ev:      agent.StreamEvent{Type: agent.EventSystem, SessionID: "sess-abc"},
			wantSID: "sess-abc",
		},
		{
			name:    "does not overwrite existing SessionID",
			initial: agent.TurnResult{SessionID: "existing"},
			ev:      agent.StreamEvent{Type: agent.EventSystem, SessionID: "new-id"},
			wantSID: "existing",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := agent.ApplyEvent(tc.initial, tc.ev)
			assert.Equal(t, tc.wantSID, got.SessionID)
		})
	}
}

func TestApplyEventAssistantTokenAccumulation(t *testing.T) {
	tests := []struct {
		name            string
		events          []agent.StreamEvent
		wantInput       int
		wantCachedInput int
		wantOutput      int
		wantTotal       int
	}{
		{
			name: "single assistant event accumulates tokens",
			events: []agent.StreamEvent{
				{
					Type: agent.EventAssistant,
					Usage: agent.UsageSnapshot{
						InputTokens:       100,
						CachedInputTokens: 20,
						OutputTokens:      50,
					},
				},
			},
			wantInput:       100,
			wantCachedInput: 20,
			wantOutput:      50,
			wantTotal:       150,
		},
		{
			name: "multiple assistant events accumulate tokens",
			events: []agent.StreamEvent{
				{
					Type: agent.EventAssistant,
					Usage: agent.UsageSnapshot{
						InputTokens:  100,
						OutputTokens: 50,
					},
				},
				{
					Type: agent.EventAssistant,
					Usage: agent.UsageSnapshot{
						InputTokens:       200,
						CachedInputTokens: 30,
						OutputTokens:      80,
					},
				},
			},
			wantInput:       300,
			wantCachedInput: 30,
			wantOutput:      130,
			wantTotal:       430,
		},
		{
			name: "InProgress=true does NOT accumulate tokens",
			events: []agent.StreamEvent{
				{
					Type:       agent.EventAssistant,
					InProgress: true,
					Usage: agent.UsageSnapshot{
						InputTokens:  999,
						OutputTokens: 999,
					},
				},
			},
			wantInput:  0,
			wantOutput: 0,
			wantTotal:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := agent.TurnResult{}
			for _, ev := range tc.events {
				r = agent.ApplyEvent(r, ev)
			}
			assert.Equal(t, tc.wantInput, r.InputTokens, "InputTokens")
			assert.Equal(t, tc.wantCachedInput, r.CachedInputTokens, "CachedInputTokens")
			assert.Equal(t, tc.wantOutput, r.OutputTokens, "OutputTokens")
			assert.Equal(t, tc.wantTotal, r.TotalTokens, "TotalTokens")
		})
	}
}

func TestApplyEventAssistantTextBlocks(t *testing.T) {
	tests := []struct {
		name          string
		events        []agent.StreamEvent
		wantLastText  string
		wantAllBlocks []string
	}{
		{
			name: "single event with text appends to AllTextBlocks and sets LastText",
			events: []agent.StreamEvent{
				{
					Type:       agent.EventAssistant,
					TextBlocks: []string{"hello"},
				},
			},
			wantLastText:  "hello",
			wantAllBlocks: []string{"hello"},
		},
		{
			name: "multiple text blocks: LastText is last block",
			events: []agent.StreamEvent{
				{
					Type:       agent.EventAssistant,
					TextBlocks: []string{"first", "second"},
				},
			},
			wantLastText:  "second",
			wantAllBlocks: []string{"first", "second"},
		},
		{
			name: "InProgress=true does NOT append text",
			events: []agent.StreamEvent{
				{
					Type:       agent.EventAssistant,
					InProgress: true,
					TextBlocks: []string{"ignored"},
				},
			},
			wantLastText:  "",
			wantAllBlocks: nil,
		},
		{
			name: "sequential events accumulate AllTextBlocks",
			events: []agent.StreamEvent{
				{Type: agent.EventAssistant, TextBlocks: []string{"block1"}},
				{Type: agent.EventAssistant, TextBlocks: []string{"block2", "block3"}},
			},
			wantLastText:  "block3",
			wantAllBlocks: []string{"block1", "block2", "block3"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := agent.TurnResult{}
			for _, ev := range tc.events {
				r = agent.ApplyEvent(r, ev)
			}
			assert.Equal(t, tc.wantLastText, r.LastText)
			assert.Equal(t, tc.wantAllBlocks, r.AllTextBlocks)
		})
	}
}

func TestApplyEventResult(t *testing.T) {
	tests := []struct {
		name            string
		initial         agent.TurnResult
		ev              agent.StreamEvent
		wantFailed      bool
		wantFailureText string
		wantResultText  string
		wantInputReq    bool
		wantInput       int
		wantOutput      int
		wantTotal       int
		wantSID         string
	}{
		{
			name: "success result accumulates tokens and sets ResultText",
			ev: agent.StreamEvent{
				Type:       agent.EventResult,
				SessionID:  "sess-r",
				ResultText: "task done",
				Usage:      agent.UsageSnapshot{InputTokens: 10, OutputTokens: 5},
			},
			wantFailed:     false,
			wantResultText: "task done",
			wantInput:      10,
			wantOutput:     5,
			wantTotal:      15,
			wantSID:        "sess-r",
		},
		{
			name: "error result sets Failed and FailureText",
			ev: agent.StreamEvent{
				Type:       agent.EventResult,
				IsError:    true,
				ResultText: "something went wrong",
				Usage:      agent.UsageSnapshot{InputTokens: 5, OutputTokens: 2},
			},
			wantFailed:      true,
			wantFailureText: "something went wrong",
			wantInput:       5,
			wantOutput:      2,
			wantTotal:       7,
		},
		{
			name: "IsInputRequired sets InputRequired",
			ev: agent.StreamEvent{
				Type:            agent.EventResult,
				IsError:         true,
				IsInputRequired: true,
				ResultText:      "waiting for input",
			},
			wantFailed:      true,
			wantInputReq:    true,
			wantFailureText: "waiting for input",
		},
		{
			name:    "result does not overwrite empty SessionID when ev.SessionID is empty",
			initial: agent.TurnResult{SessionID: "existing"},
			ev: agent.StreamEvent{
				Type:      agent.EventResult,
				SessionID: "",
			},
			wantSID: "existing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := agent.ApplyEvent(tc.initial, tc.ev)
			assert.Equal(t, tc.wantFailed, got.Failed, "Failed")
			assert.Equal(t, tc.wantFailureText, got.FailureText, "FailureText")
			assert.Equal(t, tc.wantResultText, got.ResultText, "ResultText")
			assert.Equal(t, tc.wantInputReq, got.InputRequired, "InputRequired")
			assert.Equal(t, tc.wantInput, got.InputTokens, "InputTokens")
			assert.Equal(t, tc.wantOutput, got.OutputTokens, "OutputTokens")
			assert.Equal(t, tc.wantTotal, got.TotalTokens, "TotalTokens")
			if tc.wantSID != "" {
				assert.Equal(t, tc.wantSID, got.SessionID, "SessionID")
			}
		})
	}
}

func TestApplyEventChained(t *testing.T) {
	// system → multiple assistant → result
	events := []agent.StreamEvent{
		{Type: agent.EventSystem, SessionID: "sess-chain"},
		{
			Type:       agent.EventAssistant,
			TextBlocks: []string{"thinking…"},
			Usage:      agent.UsageSnapshot{InputTokens: 100, OutputTokens: 40},
		},
		{
			Type:       agent.EventAssistant,
			TextBlocks: []string{"done"},
			Usage:      agent.UsageSnapshot{InputTokens: 50, OutputTokens: 20},
		},
		{
			Type:       agent.EventResult,
			ResultText: "all good",
			Usage:      agent.UsageSnapshot{InputTokens: 10, OutputTokens: 5},
		},
	}

	r := agent.TurnResult{}
	for _, ev := range events {
		r = agent.ApplyEvent(r, ev)
	}

	assert.Equal(t, "sess-chain", r.SessionID)
	assert.Equal(t, 160, r.InputTokens)
	assert.Equal(t, 65, r.OutputTokens)
	assert.Equal(t, 225, r.TotalTokens)
	assert.Equal(t, []string{"thinking…", "done"}, r.AllTextBlocks)
	assert.Equal(t, "done", r.LastText)
	assert.Equal(t, "all good", r.ResultText)
	assert.False(t, r.Failed)
}

// ─────────────────────────────────────────────────────────────────────────────

// TestPartialLineBuffering drives the production readLines (via the
// ReadLinesForTest export) instead of a private bufio.Scanner, so a
// regression in streamLineReader's cross-read line assembly actually fails
// this test. The previous version built its own scanner that never touched
// readLines/streamLineReader at all, and blocked on an unbuffered channel
// receive with no timeout — a real regression would have hung the test run
// rather than failing it (CORE-134).
func TestPartialLineBuffering(t *testing.T) {
	r, w := io.Pipe()
	type outcome struct {
		res agent.TurnResult
		err error
	}
	doneCh := make(chan outcome, 1)
	go func() {
		res, err := agent.ReadLinesForTest(r, agent.ParseLine)
		doneCh <- outcome{res, err}
	}()

	partial := `{"type":"result","subtype":"success","session_id":"s1"}`
	_, _ = fmt.Fprint(w, partial[:10])
	time.Sleep(10 * time.Millisecond)
	_, _ = fmt.Fprintln(w, partial[10:])
	require.NoError(t, w.Close())

	select {
	case out := <-doneCh:
		require.NoError(t, out.err)
		assert.Equal(t, "s1", out.res.SessionID,
			"a JSON line split across two separate Write calls must still be assembled into one complete line")
	case <-time.After(5 * time.Second):
		t.Fatal("readLines did not return after the split-write line completed and the pipe closed; " +
			"a partial-line-buffering regression must fail this test, not hang it")
	}
}
