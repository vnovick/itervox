package orchestrator

import (
	"log/slog"
	"slices"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/metrics"
)

// M3-close fix round — operator clear-breaker action (V1/BH-M3-3), remote
// zone-less resets (BH-M3-4), forward-after-send (BH-M3-5) and orphaned
// breakers (BH-M3-6).

// EventClearBackendBreaker clears one backend breaker. Identifier carries
// the breaker key (BackendHealthKey). Sent by ClearBackendBreaker.
const EventClearBackendBreaker EventType = "ClearBackendBreaker"

// ClearBackendBreaker asks the event loop to close the breaker for
// (backend, host) — the operator override for a breaker holding a backend
// the operator knows is available again. Safe from any goroutine: it only
// sends an event (non-blocking); false means the event channel was full.
func (o *Orchestrator) ClearBackendBreaker(backend, host string) bool {
	key := BackendHealthKey(backend, host)
	select {
	case o.events <- OrchestratorEvent{Type: EventClearBackendBreaker, Identifier: key}:
		slog.Info("orchestrator: clear backend breaker queued", "breaker", key)
		return true
	default:
		metrics.EventDropped() // CORE-045
		slog.Warn("orchestrator: clear backend breaker dropped, event channel full", "breaker", key)
		return false
	}
}

// applyClearBackendBreaker handles EventClearBackendBreaker on the event
// loop: deletes the breaker and every hold recorded against it (the held
// issues are re-evaluated by the next dispatch pass), then persists.
func (o *Orchestrator) applyClearBackendBreaker(state *State, key string) {
	_, existed := state.BackendHealth[key]
	delete(state.BackendHealth, key)
	for ident, h := range state.BackendLimitedHolds {
		if h.Key == key {
			delete(state.BackendLimitedHolds, ident)
		}
	}
	o.saveBackendHealthToDisk(state)
	o.logger().Warn("orchestrator: backend breaker cleared by operator", "breaker", key, "was_open", existed)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// limitForHost treats a text reset whose zone was ASSUMED as unknown when
// the run was on a remote SSH host (BH-M3-4). Codex prints the reset in the
// agent host's local time with no zone, and the parser assumed the daemon's
// zone: for a remote host that can be off by up to a day. Learning the
// host's zone would need an extra `ssh host date +%z` per host (another
// network round trip and failure mode on the worker path), so the reset is
// dropped instead: the breaker then uses the bounded cooldown and a single
// probe re-learns the limit. Explicit zones (Claude "resets 3pm (UTC)") and
// structured epochs are kept; local runs keep theirs (the daemon's zone is
// the host's). The input is never mutated.
func limitForHost(sig *agent.LimitSignal, host string) *agent.LimitSignal {
	if sig == nil || host == "" || !sig.ResetZoneAssumed || sig.ResetsAt.IsZero() {
		return sig
	}
	c := *sig
	c.ResetsAt = time.Time{}
	c.ResetZoneAssumed = false
	return &c
}

// limitForwarder decides which mid-turn api_retry throttle a worker forwards
// on EventWorkerUpdate. A signal is marked delivered only after the send
// succeeded (BH-M3-5): the update send is non-blocking and can be dropped,
// and a dropped signal must be offered again on the next progress callback.
type limitForwarder struct{ delivered *agent.LimitSignal }

// candidate returns l when it is an api_retry throttle not yet delivered.
func (f *limitForwarder) candidate(l *agent.LimitSignal) *agent.LimitSignal {
	if l == nil || l == f.delivered || l.Kind != agent.LimitKindThrottle || l.Source != agent.LimitSourceAPIRetry {
		return nil
	}
	return l
}

// sent records a delivered signal.
func (f *limitForwarder) sent(l *agent.LimitSignal) { f.delivered = l }

// pruneOrphanBreakers drops breakers nothing can ever probe (BH-M3-6): a
// breaker for an SSH host that is no longer configured (any status), and a
// non-limited (probing or warning) breaker for a backend no dispatch path
// uses any more (agent.command, enabled profiles, the fallback chain). A
// still-limited breaker for an unused backend is kept until it expires into
// probing. When a wrapper command's backend cannot be determined every
// backend counts as used. Event loop only; returns whether it changed.
func (o *Orchestrator) pruneOrphanBreakers(state *State) bool {
	if len(state.BackendHealth) == 0 {
		return false
	}
	o.cfgMu.RLock()
	hosts := slices.Clone(o.cfg.Agent.SSHHosts)
	used := map[string]struct{}{}
	anyBackend := false
	addCommand := func(p config.AgentProfile) {
		if b := config.ProfileRunsBackend(p, o.cfg.Agent.Command); b != "" {
			used[b] = struct{}{}
		} else {
			anyBackend = true
		}
	}
	addCommand(config.AgentProfile{Command: o.cfg.Agent.Command, Backend: o.cfg.Agent.Backend})
	for _, p := range o.cfg.Agent.Profiles {
		if config.ProfileEnabled(p) {
			addCommand(p)
		}
	}
	o.cfgMu.RUnlock()
	if fb := o.cfg.Agent.BackendFallback; fb.Enabled {
		for _, b := range fb.Chain {
			used[b] = struct{}{}
		}
	}
	changed := false
	for key, e := range state.BackendHealth {
		hostGone := e.Host != "" && !slices.Contains(hosts, e.Host)
		_, backendUsed := used[e.Backend]
		unused := !anyBackend && !backendUsed && e.Status != BackendStatusLimited
		if hostGone || unused {
			delete(state.BackendHealth, key)
			changed = true
			slog.Info("orchestrator: pruned orphaned backend breaker", "breaker", key,
				"host_removed", hostGone, "backend_unused", unused)
		}
	}
	return changed
}
