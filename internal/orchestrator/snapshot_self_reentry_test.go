package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSnapshotCarriesSelfReentryDropCounter locks the gaps_11 G-11 surfacing
// contract: the event-loop counter AutomationDropsSelfReentryTotal must flow
// through storeSnap into the published snapshot (State is copied by value, so
// scalar fields ride along automatically — this test prevents a future
// refactor to a pointer-/map-based snapshot from silently dropping it).
func TestSnapshotCarriesSelfReentryDropCounter(t *testing.T) {
	t.Parallel()
	o := &Orchestrator{}

	state := State{AutomationDropsSelfReentryTotal: 7}
	o.storeSnap(state)

	snap := o.Snapshot()
	require.Equal(t, uint64(7), snap.AutomationDropsSelfReentryTotal,
		"snapshot must carry the self-reentry drop counter")

	// Counter increments in the event loop must be visible on the next publish.
	state.AutomationDropsSelfReentryTotal++
	o.storeSnap(state)
	require.Equal(t, uint64(8), o.Snapshot().AutomationDropsSelfReentryTotal)
}
