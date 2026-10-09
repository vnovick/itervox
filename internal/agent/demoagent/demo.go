// Package demoagent holds the scripted agent behind `itervox demo` (#76).
// It reuses the TurnResult shapes of the agenttest scenarios, but lives in
// its own package because it is compiled into the binary (agenttest's
// helpers are test-only).
package demoagent

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/agent"
)

// DemoRunner is the scripted agent behind `itervox demo` (#76). It needs no
// agent CLI: each turn streams a few realistic log lines and progress
// updates, "opens" a fake pull request, and returns. Issues are told apart
// by the workspace directory name (the issue identifier), and a few of them
// follow a different script on their first turn so the board shows every
// path:
//
//   - DEMO-n with n%5 == 2 asks for input first, then finishes after the reply;
//   - DEMO-n with n%5 == 4 fails its first turn, then succeeds on the retry;
//   - every other issue succeeds on its first turn.
type DemoRunner struct {
	// Step is the pause between streamed events; a turn takes about 6 steps.
	Step time.Duration
	// PRBaseURL prefixes the fake pull request URLs.
	PRBaseURL string

	mu    sync.Mutex
	turns map[string]int
}

// NewDemoRunner returns a DemoRunner with the given step.
func NewDemoRunner(step time.Duration) *DemoRunner {
	return &DemoRunner{Step: step, PRBaseURL: "https://example.com/itervox-demo/pull/", turns: map[string]int{}}
}

// Turns reports how many turns identifier has run.
func (r *DemoRunner) Turns(identifier string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turns[identifier]
}

// RunTurn implements agent.Runner.
func (r *DemoRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, _ string, workspacePath, _ string, _ string, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	identifier := filepath.Base(workspacePath)
	r.mu.Lock()
	r.turns[identifier]++
	turn := r.turns[identifier]
	r.mu.Unlock()

	sid := fmt.Sprintf("demo-%s-%d", strings.ToLower(identifier), turn)
	if sessionID != nil && *sessionID != "" {
		sid = *sessionID // a resume continues its session
	}
	res := agent.TurnResult{SessionID: sid}
	n := demoNumber(identifier)

	step := func(msg string, args ...any) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.Step):
		}
		if log != nil {
			log.Info(msg, append([]any{"session_id", sid}, args...)...)
		}
		res.InputTokens += 900
		res.OutputTokens += 160
		res.TotalTokens = res.InputTokens + res.OutputTokens
		if text, ok := demoArg(args, "text"); ok {
			res.LastText = text
			res.AllTextBlocks = append(res.AllTextBlocks, text)
		}
		if onProgress != nil {
			onProgress(res)
		}
		return nil
	}

	script := []struct {
		msg  string
		args []any
	}{
		{"claude: session started", nil},
		{"claude: text", []any{"text", "Reading " + identifier + " and the code it touches."}},
		{"claude: action", []any{"tool", "Read", "description", "internal/billing/invoice.go"}},
		{"claude: action", []any{"tool", "Edit", "description", "internal/billing/invoice.go"}},
		{"claude: action", []any{"tool", "Bash", "description", "make test"}},
	}
	for _, s := range script {
		if err := step(s.msg, s.args...); err != nil {
			return res, err
		}
	}

	switch {
	case n%5 == 2 && turn == 1:
		question := "Should the invoice total round per line or once at the end? The issue does not say."
		if err := step("claude: text", "text", question); err != nil {
			return res, err
		}
		res.InputRequired = true
		res.ResultText = question
		return res, nil
	case n%5 == 4 && turn == 1:
		if err := step("claude: action_detail", "tool", "Bash", "status", "failed", "exit_code", "1"); err != nil {
			return res, err
		}
		res.Failed = true
		res.FailureText = "make test: 2 tests failed in internal/billing (demo failure; the retry passes)"
		return res, nil
	}

	url := r.PRBaseURL + strconv.Itoa(100+n)
	if err := step("claude: text", "text", "Tests pass. Opening a pull request."); err != nil {
		return res, err
	}
	if log != nil {
		log.Info("worker: pr_opened", "session_id", sid, "url", url)
	}
	res.ResultText = "Implemented " + identifier + "; pull request " + url
	if err := step("claude: turn done", "input_tokens", res.InputTokens, "output_tokens", res.OutputTokens); err != nil {
		return res, err
	}
	return res, nil
}

// demoNumber returns n for "DEMO-n" (0 when it does not parse).
func demoNumber(identifier string) int {
	_, num, ok := strings.Cut(identifier, "-")
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(num)
	return n
}

func demoArg(args []any, key string) (string, bool) {
	for i := 0; i+1 < len(args); i += 2 {
		if k, ok := args[i].(string); ok && k == key {
			s, ok := args[i+1].(string)
			return s, ok
		}
	}
	return "", false
}
