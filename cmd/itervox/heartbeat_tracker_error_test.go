package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// CORE-044: HEARTBEAT "Last error" shows a tracker outage, which it never
// looked at before (it read only ConfigInvalid, Retrying and the automation
// queue).
func TestHeartbeatLastErrorShowsLastTrackerError(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	content := renderHeartbeat(server.StateSnapshot{
		LastTrackerError: &server.TrackerErrorRow{
			At: now.Add(-time.Minute), Op: "poll", Kind: "outage",
			Message: "linear: dial tcp: connection refused", ConsecutiveFailures: 3,
		},
		Retrying: []server.RetryRow{{Identifier: "ISS-1", Error: "agent failed"}},
	}, heartbeatOptions{}, now)

	assert.Contains(t, content,
		"- Last error: tracker poll outage (3 consecutive) at 2026-09-26T09:59:00Z: linear: dial tcp: connection refused")
}

func TestHeartbeatLastErrorShowsRateLimitReset(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	reset := now.Add(10 * time.Minute)
	content := renderHeartbeat(server.StateSnapshot{
		LastTrackerError: &server.TrackerErrorRow{
			At: now, Op: "poll", Kind: "rate_limited", Message: "tracker: linear rate limited", ResetAt: &reset,
		},
	}, heartbeatOptions{}, now)
	assert.Contains(t, content, "- Last error: tracker poll rate_limited until 2026-09-26T10:10:00Z at 2026-09-26T10:00:00Z: tracker: linear rate limited")
}

// A stuck outbox write is visible without a new event: the degraded entry
// that failed most recently wins over the retry queue.
func TestHeartbeatLastErrorShowsNewestDegradedOutboxEntry(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	older, newer := now.Add(-time.Hour), now.Add(-time.Minute)
	content := renderHeartbeat(server.StateSnapshot{
		OutboxEntries: []server.OutboxEntryRow{
			{ID: "a", Kind: "update_state", Identifier: "ENG-1", Degraded: true, LastError: "old failure", LastFailedAt: &older},
			{ID: "b", Kind: "create_comment", Identifier: "ENG-2", Degraded: true, LastError: "new failure", LastFailedAt: &newer},
			{ID: "c", Kind: "create_comment", Identifier: "ENG-3", Degraded: false, LastError: "not degraded", LastFailedAt: &now},
		},
		Retrying: []server.RetryRow{{Identifier: "ISS-1", Error: "agent failed"}},
	}, heartbeatOptions{}, now)
	assert.Contains(t, content, "- Last error: outbox create_comment ENG-2 degraded: new failure")
}

func TestTrackerErrorRowFromState(t *testing.T) {
	assert.Nil(t, trackerErrorRow(orchestrator.State{}), "no error recorded → field omitted")

	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	row := trackerErrorRow(orchestrator.State{
		LastTrackerError: orchestrator.TrackerErrorInfo{
			At: at, Op: orchestrator.TrackerErrorOpPoll, Kind: orchestrator.TrackerErrorKindOutage,
			Message: "Authorization: Bearer lin_api_" + "0123456789abcdef0123456789abcdef0123",
		},
		ConsecutivePollFailures: 4,
	})
	require.NotNil(t, row)
	assert.Equal(t, "poll", row.Op)
	assert.Equal(t, "outage", row.Kind)
	assert.Equal(t, 4, row.ConsecutiveFailures)
	assert.Nil(t, row.ResetAt)
	assert.NotContains(t, row.Message, "0123456789abcdef0123456789abcdef0123", "tracker error text is redacted")
}
