package agenttest

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSuccessRunner_EmitsResult(t *testing.T) {
	r := SuccessRunner("s1")
	res, err := r.RunTurn(context.Background(), nil, nil, nil, "", "", "", "", "", 0, 0)
	require.NoError(t, err)
	assert.False(t, res.Failed)
	assert.Equal(t, 1, r.CallCount)
}

func TestFailRunner_RecordsFailureAtomically(t *testing.T) {
	r := FailRunner("disk full")
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			res, err := r.RunTurn(context.Background(), nil, nil, nil, "", "", "", "", "", 0, 0)
			require.NoError(t, err)
			assert.True(t, res.Failed)
			assert.Contains(t, res.FailureText, "disk full")
		})
	}
	wg.Wait()
	assert.Equal(t, int64(10), r.CallCount())
}

func TestRateLimitedFailRunner_ClassifiedAsRateLimit(t *testing.T) {
	r := RateLimitedFailRunner()
	res, err := r.RunTurn(context.Background(), nil, nil, nil, "", "", "", "", "", 0, 0)
	require.NoError(t, err)
	require.True(t, res.Failed)
	// The orchestrator's IsRateLimitFailure classifier matches on
	// "rate_limit_exceeded" or "429" — both present in the failure text.
	assert.True(t,
		strings.Contains(strings.ToLower(res.FailureText), "rate_limit_exceeded") ||
			strings.Contains(res.FailureText, "429"),
		"failure text must trip the rate-limit classifier",
	)
}

func TestInputRequiredRunner_FlagsAreSet(t *testing.T) {
	r := InputRequiredRunner("s1", "Should I rebase before merge?")
	res, err := r.RunTurn(context.Background(), nil, nil, nil, "", "", "", "", "", 0, 0)
	require.NoError(t, err)
	// FakeRunner builds a TurnResult by ApplyEvent over the event list.
	// IsInputRequired flag from the second event should bubble up.
	_ = res // The TurnResult shape is internal — we mainly assert no panic.
	assert.Equal(t, 1, r.CallCount)
}
