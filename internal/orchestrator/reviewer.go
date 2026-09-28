package orchestrator

import (
	"fmt"

	"github.com/vnovick/itervox/internal/metrics"
)

// DispatchReviewer sends a reviewer dispatch event to the event loop for the
// given issue identifier. The reviewer runs as a regular worker with
// Kind="reviewer" using the configured ReviewerProfile (or the specified profile).
// Returns an error if no reviewer profile is configured.
// Safe to call from any goroutine.
func (o *Orchestrator) DispatchReviewer(identifier string) error {
	if o.isDraining() { // CORE-057
		return ErrDraining
	}
	o.cfgMu.RLock()
	profile := o.cfg.Agent.ReviewerProfile
	o.cfgMu.RUnlock()

	if profile == "" {
		return fmt.Errorf("reviewer: no reviewer_profile configured")
	}

	select {
	case o.events <- OrchestratorEvent{
		Type:            EventDispatchReviewer,
		Identifier:      identifier,
		ReviewerProfile: profile,
	}:
		return nil
	default:
		metrics.EventDropped() // CORE-045
		return fmt.Errorf("reviewer: event channel full")
	}
}
