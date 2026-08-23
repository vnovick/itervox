package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// ORCH-3: Snapshot() must not alias the live PR-dispatch ledgers.
func TestSnapshotDeepCopiesPRDispatchLedgers(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 1
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	state.PROpenedDispatched["k1"] = struct{}{}
	if state.PRMergedDispatched == nil {
		t.Fatal("NewState must initialize PRMergedDispatched (ORCH-3 init asymmetry)")
	}
	state.PRMergedDispatched["k2"] = struct{}{}
	o.storeSnap(state)
	snap := o.Snapshot()
	state.PROpenedDispatched["k3"] = struct{}{}
	state.PRMergedDispatched["k4"] = struct{}{}
	require.NotContains(t, snap.PROpenedDispatched, "k3", "snapshot aliases live PROpenedDispatched")
	require.NotContains(t, snap.PRMergedDispatched, "k4", "snapshot aliases live PRMergedDispatched")
}

// wave-2 polish Task 4 — copyDependencyCycles/copyDependencyAttention used to
// return nil for a nil input, unlike every other Snapshot() copy helper
// (copyRunningMap, copyAutomationQueueMap, copyDependencyAuditMap,
// copyIssueStatusHistoryMap, ...), which all `make(..., len(m))` and so
// return an empty non-nil value regardless of whether the source was nil.
// That inconsistency meant a snapshot with no cycles/attention entries
// serialized those two fields as JSON `null` while every sibling collection
// serialized `[]`/`{}`. Pin the corrected, consistent behavior directly.
func TestCopyDependencyCyclesNilInputReturnsEmptyNonNil(t *testing.T) {
	got := copyDependencyCycles(nil)
	require.NotNil(t, got, "nil input must return an empty non-nil slice, matching the map-copy siblings")
	require.Empty(t, got)
}

func TestCopyDependencyAttentionNilInputReturnsEmptyNonNil(t *testing.T) {
	got := copyDependencyAttention(nil)
	require.NotNil(t, got, "nil input must return an empty non-nil slice, matching the map-copy siblings")
	require.Empty(t, got)
}
