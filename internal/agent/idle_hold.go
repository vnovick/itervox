package agent

import "context"

// IdleHold reports whether a run is deliberately silent right now: its agent
// is blocked on something outside the CLI that is expected to take a while,
// such as a guardrail shim waiting for an operator's approval (#204). While
// it reports true, readLines does not end the turn on the idle read deadline
// (agent.read_timeout_ms); the turn context (agent.turn_timeout_ms) still
// bounds the turn. It is called from the runner's read loop, so it must be
// safe from any goroutine and cheap.
type IdleHold func() bool

type idleHoldKey struct{}

// WithIdleHold returns ctx carrying hold for the runner. It travels in the
// turn's context the same way RunEnv does; a nil hold is ignored.
func WithIdleHold(ctx context.Context, hold IdleHold) context.Context {
	if hold == nil {
		return ctx
	}
	return context.WithValue(ctx, idleHoldKey{}, hold)
}

// idleHeld reports whether ctx's IdleHold (if any) holds the idle deadline.
func idleHeld(ctx context.Context) bool {
	hold, _ := ctx.Value(idleHoldKey{}).(IdleHold)
	return hold != nil && hold()
}
