package main

import (
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// pendingStateOverlay is the local tracker store's hook for queued, unsent
// state moves (#115); store.Store implements it.
type pendingStateOverlay interface {
	SetPendingStates(fn func() map[string]string)
}

// wireCoalescedPush configures the outbox's coalesced push (#115): writes
// wait tracker.push_interval_ms in their issue's batch, and the local tracker
// store, when tr is one, lays the batch's queued state moves over its list
// reads so the board shows them at once. Called only with the outbox on.
func wireCoalescedPush(cfg *config.Config, ob *outbox.Outbox, tr tracker.Tracker) {
	interval := time.Duration(cfg.Tracker.PushIntervalMs) * time.Millisecond
	ob.SetPushInterval(interval)
	overlay, ok := tr.(pendingStateOverlay)
	if ok {
		overlay.SetPendingStates(ob.PendingStates)
	}
	slog.Info("outbox: coalesced push", "push_interval_ms", cfg.Tracker.PushIntervalMs, "store_overlay", ok)
}
