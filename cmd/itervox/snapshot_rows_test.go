package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
)

// TestDependencyAuditRowsCarryDegradedState verifies dependencyAuditRows
// (cmd/itervox/snapshot_rows.go) marks a row Degraded once its
// ConsecutiveFailures reaches orchestrator.DependencyRefreshDegradedThreshold
// (Task 7). Dispatch behaviour is unaffected — this only feeds the operator
// signal that the row's blocked/unblocked status may be stale.
func TestDependencyAuditRowsCarryDegradedState(t *testing.T) {
	rows := dependencyAuditRows(map[string]*orchestrator.DependencyAuditEntry{
		"a": {
			Identifier:          "ENG-1",
			IssueState:          "Todo",
			Status:              orchestrator.DependencyAuditBlocked,
			ConsecutiveFailures: 4,
		},
		"b": {
			Identifier:          "ENG-2",
			IssueState:          "Todo",
			Status:              orchestrator.DependencyAuditBlocked,
			ConsecutiveFailures: 1,
		},
	})

	require.Len(t, rows, 2)
	assert.True(t, rows[0].Degraded, "4 consecutive failures is past the threshold of 3")
	assert.False(t, rows[1].Degraded, "1 failure is not yet degraded")
}

// TestDegradedDependencyAuditCount verifies the snapshot-level aggregate
// (cmd/itervox/main.go's DepsRefreshDegradedCount) counts only rows that
// crossed the threshold, ignores nil entries, and returns 0 for an empty map.
func TestDegradedDependencyAuditCount(t *testing.T) {
	assert.Equal(t, 0, degradedDependencyAuditCount(nil))

	count := degradedDependencyAuditCount(map[string]*orchestrator.DependencyAuditEntry{
		"a": {Identifier: "ENG-1", ConsecutiveFailures: 4},
		"b": {Identifier: "ENG-2", ConsecutiveFailures: 3},
		"c": {Identifier: "ENG-3", ConsecutiveFailures: 2},
		"d": nil,
	})
	assert.Equal(t, 2, count, "ENG-1 (4) and ENG-2 (3, at the threshold) are degraded; ENG-3 (2) and the nil entry are not")
}

// TestOutboxEntryRowsCarryRateLimitedUntil verifies outboxEntryRows carries
// outbox.Entry.RateLimitedUntil onto the wire row as a *time.Time (Task 9,
// write-ahead-outbox design "Surfaces").
func TestOutboxEntryRowsCarryRateLimitedUntil(t *testing.T) {
	reset := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	rows := outboxEntryRows([]outbox.Entry{{
		ID: "1", Kind: outbox.KindCreateComment, Identifier: "ENG-1",
		RateLimitedUntil: reset, RateLimitedAttempts: 2,
	}})

	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].RateLimitedUntil)
	assert.True(t, rows[0].RateLimitedUntil.Equal(reset))
}

// TestOutboxEntryRowsOmitZeroRateLimitedUntil verifies a never-rate-limited
// entry (zero RateLimitedUntil) omits the field rather than serialising the
// zero time, matching the *time.Time posture used elsewhere on the snapshot
// (e.g. DepsAnalyzeJobRow).
func TestOutboxEntryRowsOmitZeroRateLimitedUntil(t *testing.T) {
	rows := outboxEntryRows([]outbox.Entry{{
		ID: "1", Kind: outbox.KindUpdateState, Identifier: "ENG-1", TargetState: "Done",
	}})

	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].RateLimitedUntil, "a never-rate-limited entry omits the field")
}
