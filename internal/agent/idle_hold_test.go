package agent

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	holdInitLine   = `{"type":"system","subtype":"init","session_id":"sess-hold"}` + "\n"
	holdResultLine = `{"type":"result","subtype":"success","is_error":false,"session_id":"sess-hold","result":"deployed"}` + "\n"
)

// A run whose agent is silent because a guardrail shim waits for an
// operator's approval (an IdleHold reporting true) is not ended by the idle
// read deadline, however many deadlines pass; once the hold ends and the
// CLI prints its result, the turn completes normally.
func TestReadLinesIdleHoldSuspendsReadTimeout(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	var pending atomic.Bool
	pending.Store(true)
	ctx := WithIdleHold(t.Context(), pending.Load)

	go func() {
		_, _ = pw.Write([]byte(holdInitLine))
		time.Sleep(1 * time.Second) // five read deadlines of silence
		pending.Store(false)        // the operator approved
		_, _ = pw.Write([]byte(holdResultLine))
		_ = pw.Close()
	}()
	res, err := readLines(ctx, nopLogger{}, nil, pr, 200, "claude", ParseLine)
	require.NoError(t, err, "a pending approval must not end the turn with a read timeout")
	assert.Equal(t, "sess-hold", res.SessionID)
	assert.False(t, res.Failed)
}

// Control: an agent that is idle with no pending approval still ends the
// turn on the read deadline, both with an IdleHold that reports false and
// with none in the context.
func TestReadLinesIdleWithoutHoldStillTimesOut(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"hold reports false": WithIdleHold(t.Context(), func() bool { return false }),
		"no hold":            t.Context(),
		"nil hold":           WithIdleHold(t.Context(), nil),
	} {
		t.Run(name, func(t *testing.T) {
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			go func() { _, _ = pw.Write([]byte(holdInitLine)) }()
			start := time.Now()
			_, err := readLines(ctx, nopLogger{}, nil, pr, 200, "claude", ParseLine)
			require.Error(t, err)
			assert.Equal(t, "agent: read timeout after 200ms idle", err.Error())
			assert.Less(t, time.Since(start), 2*time.Second)
		})
	}
}

// After a hold ends, a CLI that stays silent still times out, and it gets a
// full read deadline from the end of the hold, not the remainder of the
// period in which the hold ended.
func TestReadLinesFullReadDeadlineAfterHold(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	var pending atomic.Bool
	pending.Store(true)
	ctx := WithIdleHold(t.Context(), pending.Load)
	var endedAt atomic.Int64
	go func() {
		_, _ = pw.Write([]byte(holdInitLine))
		time.Sleep(450 * time.Millisecond)
		endedAt.Store(time.Now().UnixNano())
		pending.Store(false)
	}()
	_, err := readLines(ctx, nopLogger{}, nil, pr, 200, "claude", ParseLine)
	require.Error(t, err)
	assert.Equal(t, "agent: read timeout after 200ms idle", err.Error())
	silentFor := time.Since(time.Unix(0, endedAt.Load()))
	assert.GreaterOrEqual(t, silentFor, 200*time.Millisecond, "the CLI gets a full deadline after the hold")
}

// The hold suspends only the idle read deadline: the turn context
// (agent.turn_timeout_ms) still ends a turn whose approval never comes.
func TestReadLinesIdleHoldStillBoundedByTurnContext(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	turnCtx, cancel := context.WithTimeout(t.Context(), 700*time.Millisecond)
	defer cancel()
	ctx := WithIdleHold(turnCtx, func() bool { return true })
	go func() { _, _ = pw.Write([]byte(holdInitLine)) }()
	_, err := readLines(ctx, nopLogger{}, nil, pr, 200, "claude", ParseLine)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
}
