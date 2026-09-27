// Gap §4.8 — shared test doubles for orchestrator integration tests.
// Scenario constructors that wrap the existing FakeRunner with the
// most-common shapes: success, failure, rate-limit failure, input-required.
//
// The orchestrator's `alwaysFailRunner` (in retry_test.go) predates this
// file and stays for back-compat. New tests should use these scenario
// constructors so all integration tests share the same behavior surface.

package agenttest

import (
	"context"
	"sync/atomic"

	"github.com/vnovick/itervox/internal/agent"
)

// SuccessRunner emits a single successful turn (system+result events).
func SuccessRunner(sessionID string) *FakeRunner {
	return NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: sessionID},
		{Type: "result", SessionID: sessionID},
	})
}

// FailRunner returns a runner whose every RunTurn invocation reports a
// non-zero-token failure with the given error message. Mirrors the
// `alwaysFailRunner` pattern from retry_test.go but exposed as a reusable
// helper. CallCount is atomic so tests can read it concurrently with
// orchestrator goroutines.
func FailRunner(failureText string) *CountingFailRunner {
	return &CountingFailRunner{failureText: failureText}
}

// RateLimitedFailRunner is a FailRunner whose failure text contains
// "rate_limit_exceeded" — guaranteed to be classified as rate-limited
// by `IsRateLimitFailure`. Use this in integration tests that exercise
// the rate_limited automation dispatch path.
func RateLimitedFailRunner() *CountingFailRunner {
	return FailRunner("Error: HTTP 429: rate_limit_exceeded — please retry later")
}

// InputRequiredRunner emits the event sequence the real parsers produce for a
// vendor-signalled input request: an assistant message carrying the agent's
// question, then an error result event flagged IsInputRequired (what
// ParseLine emits for a Claude `result` with is_error and an input-request
// message, and ParseCodexLine for a Codex `turn.failed`). ApplyEvent turns
// that into a TurnResult with Failed and InputRequired both set; the worker
// routes it to TerminalInputRequired and queues an InputRequiredEntry
// (CORE-164). There is no "input_required" stream event type — ApplyEvent
// would silently drop one (CORE-138/142).
func InputRequiredRunner(sessionID, question string) *FakeRunner {
	return NewFakeRunner([]agent.StreamEvent{
		{Type: agent.EventSystem, SessionID: sessionID},
		{
			Type:       agent.EventAssistant,
			SessionID:  sessionID,
			Message:    question,
			TextBlocks: []string{question},
			Usage:      agent.UsageSnapshot{InputTokens: 100, OutputTokens: 20},
		},
		{
			Type:            agent.EventResult,
			SessionID:       sessionID,
			IsError:         true,
			IsInputRequired: true,
			ResultText:      "Human turn required",
		},
	})
}

// CountingFailRunner is a fail-every-turn double that records its call
// count atomically. Used by integration tests that need to verify retry
// budgets and exhaustion paths.
type CountingFailRunner struct {
	failureText string
	calls       atomic.Int64
}

// CallCount returns the number of RunTurn invocations observed. Safe to
// call concurrently with the orchestrator's worker goroutines.
func (r *CountingFailRunner) CallCount() int64 {
	return r.calls.Load()
}

// RunTurn fails every turn with non-zero tokens (so the orchestrator
// treats it as a real failure, not a clean session-end empty result).
func (r *CountingFailRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.calls.Add(1)
	return agent.TurnResult{
		Failed:       true,
		FailureText:  r.failureText,
		InputTokens:  100,
		OutputTokens: 50,
		TotalTokens:  150,
	}, nil
}
