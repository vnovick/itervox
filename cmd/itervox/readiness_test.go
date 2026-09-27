package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-043: /ready's tracker_rate_limited_until is read from the shared
// tracker rate-limit gate (CORE-119 made its recorded reset trustworthy), and
// config_invalid from the reload loop's published status.
func TestReadinessSignalsReadRateLimitGateAndConfigInvalid(t *testing.T) {
	const adapter = "ready-signals-test-adapter"
	gate := tracker.SharedRateLimitGate()
	gate.Clear(adapter)
	t.Cleanup(func() { gate.Clear(adapter) })

	started := time.Now().Add(-time.Minute)
	r := orchestrator.Readiness{LoopStarted: true, LastLoopIdle: time.Now(), PollInterval: 30 * time.Second, LastPollOK: true, PollFailureThreshold: 3}

	sig := readinessSignals(r, started, adapter, nil)
	assert.Nil(t, sig.RateLimitedUntil, "closed gate → null")
	assert.False(t, sig.ConfigInvalid)
	assert.Equal(t, started, sig.StartedAt)
	assert.True(t, sig.LastPollOK)
	assert.Equal(t, 3, sig.PollFailureThreshold)

	until := time.Now().Add(20 * time.Minute)
	gate.RecordUntil(adapter, until)
	sig = readinessSignals(r, started, adapter, &server.ConfigInvalidStatus{Error: "bad yaml"})
	require.NotNil(t, sig.RateLimitedUntil)
	assert.WithinDuration(t, until, *sig.RateLimitedUntil, time.Second)
	assert.True(t, sig.ConfigInvalid)

	resp := server.EvaluateReadiness(sig, time.Now())
	assert.True(t, resp.Ready)
	assert.True(t, resp.Degraded)
}

func TestReadinessSignalsCarryShedding(t *testing.T) {
	sig := readinessSignals(orchestrator.Readiness{LoopStarted: true, LastLoopIdle: time.Now(), PollShedding: true}, time.Now(), "ready-shed-adapter", nil)
	assert.True(t, sig.PollShedding)
	assert.True(t, server.EvaluateReadiness(sig, time.Now()).Degraded)
}
