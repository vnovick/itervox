package main

import (
	"time"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// readinessSignals assembles the /api/v1/ready inputs (CORE-043) from the
// orchestrator's lock-free readiness atomics, the tracker rate-limit gate
// (which takes only its own mutex — never cfgMu) and the reload loop's
// published config status. adapter is cfg.Tracker.Kind, read-only after
// startup. Kept free of cfgMu and snapshot building so a probe can never
// block behind a settings save or the event loop.
func readinessSignals(r orchestrator.Readiness, startedAt time.Time, adapter string, configInvalid *server.ConfigInvalidStatus) server.ReadinessSignals {
	sig := server.ReadinessSignals{
		StartedAt:               startedAt,
		LoopStarted:             r.LoopStarted,
		LastLoopIdle:            r.LastLoopIdle,
		TickStarted:             r.TickStarted,
		PollInterval:            r.PollInterval,
		LastPollOK:              r.LastPollOK,
		PollRateLimited:         r.PollRateLimited,
		PollShedding:            r.PollShedding,
		ConsecutivePollFailures: r.ConsecutivePollFailures,
		PollFailureThreshold:    r.PollFailureThreshold,
		ConfigInvalid:           configInvalid != nil,
		Draining:                r.Draining, // CORE-057
	}
	if until, open := tracker.SharedRateLimitGate().OpenUntil(adapter); open {
		u := until.UTC()
		sig.RateLimitedUntil = &u
	}
	return sig
}
