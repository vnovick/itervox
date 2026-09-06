// Package agenttest provides test doubles for the agent package.
package agenttest

import (
	"context"
	"sync"

	"github.com/vnovick/itervox/internal/agent"
)

// FakeRunner is a test double that replays scripted StreamEvents without
// spawning a real subprocess.
type FakeRunner struct {
	mu        sync.Mutex
	Events    []agent.StreamEvent
	Stall     bool // if true, blocks until ctx cancelled
	CallCount int
	// SessionIDs records the sessionID pointer value passed to each RunTurn
	// invocation. Empty string for nil/empty pointers; the actual session ID
	// otherwise. Useful for verifying --resume behavior in tests.
	SessionIDs []string
}

// NewFakeRunner constructs a FakeRunner that will emit the given events in order.
func NewFakeRunner(events []agent.StreamEvent) *FakeRunner {
	return &FakeRunner{Events: events}
}

// RunTurn replays the scripted events and builds a TurnResult.
func (f *FakeRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int, _ agent.PermissionMode) (agent.TurnResult, error) {
	f.mu.Lock()
	f.CallCount++
	sid := ""
	if sessionID != nil {
		sid = *sessionID
	}
	f.SessionIDs = append(f.SessionIDs, sid)
	f.mu.Unlock()
	if f.Stall {
		<-ctx.Done()
		return agent.TurnResult{Failed: true}, ctx.Err()
	}
	var result agent.TurnResult
	for _, ev := range f.Events {
		result = agent.ApplyEvent(result, ev)
	}
	result.TotalTokens = result.InputTokens + result.OutputTokens
	return result, nil
}
