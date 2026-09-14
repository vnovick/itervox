package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
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
