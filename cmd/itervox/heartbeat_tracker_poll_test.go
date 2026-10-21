package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// #187: the snapshot row and the HEARTBEAT "## Tracker" section show a poll
// that is running long ("tick running for Xs") or degraded by the tracker
// breaker.

func TestTrackerPollRowReportsSlowAndDegraded(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	s := orchestrator.State{PollIntervalMs: 30_000}
	s.TrackerPoll = orchestrator.TrackerPollState{InFlight: true, StartedAt: now.Add(-50 * time.Second)}
	row := trackerPollRow(s, now)
	require.NotNil(t, row)
	assert.True(t, row.InFlight)
	assert.Equal(t, int64(50_000), row.RunningForMs)
	assert.Equal(t, int64(45_000), row.SlowAfterMs, "interval plus half an interval")
	assert.True(t, row.Slow)
	assert.False(t, row.Degraded)

	s.TrackerPoll = orchestrator.TrackerPollState{InFlight: true, StartedAt: now.Add(-10 * time.Second)}
	assert.False(t, trackerPollRow(s, now).Slow, "a poll inside its threshold is not slow")

	open := now.Add(time.Minute)
	s.TrackerPoll = orchestrator.TrackerPollState{LastDuration: 2 * time.Second, LastFinishedAt: now.Add(-time.Second)}
	s.TrackerBreaker = orchestrator.TrackerBreakerState{ConsecutiveTransportFailures: 3, OpenUntil: open, OpenedAt: now.Add(-time.Minute)}
	row = trackerPollRow(s, now)
	assert.True(t, row.Degraded)
	require.NotNil(t, row.BreakerOpenUntil)
	assert.Equal(t, open, *row.BreakerOpenUntil)
	assert.Equal(t, 3, row.ConsecutiveTransportFailures)
}

func TestHeartbeatReportsTrackerPoll(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	open := now.Add(time.Minute)
	cases := []struct {
		name string
		row  *server.TrackerPollRow
		want string
	}{
		{"slow", &server.TrackerPollRow{InFlight: true, RunningForMs: 47_400, Slow: true}, "- Poll: slow, running for 47s"},
		{"running", &server.TrackerPollRow{InFlight: true, RunningForMs: 3_000}, "- Poll: running for 3s"},
		{"degraded", &server.TrackerPollRow{Degraded: true, ConsecutiveTransportFailures: 3, BreakerOpenUntil: &open},
			"- Poll: degraded (3 consecutive transport failures; tracker reads paused until 2026-10-20T10:01:00Z)"},
		{"ok", &server.TrackerPollRow{LastFinishedAt: &now, LastDurationMs: 840}, "- Poll: ok, last took 840ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := renderHeartbeat(server.StateSnapshot{TrackerPoll: tc.row}, heartbeatOptions{}, now)
			assert.Contains(t, content, "## Tracker\n- Kind: unknown\n- Project: unknown\n"+tc.want+"\n")
		})
	}
	assert.NotContains(t, renderHeartbeat(server.StateSnapshot{}, heartbeatOptions{}, now), "- Poll:",
		"no row (an older daemon), no line")
}
