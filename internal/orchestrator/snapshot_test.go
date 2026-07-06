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
