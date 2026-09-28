package orchestrator

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
)

// CORE-053 — backend-wide circuit breaker.
//
// State.BackendHealth holds one breaker per (backend, worker host): a limit
// seen on SSH host A says nothing about local runs or host B, which may run
// under different credentials, so the key is BackendHealthKey(backend, host)
// ("claude", "codex@build-1"). An absent key is healthy.
//
//	healthy ──quota LimitSignal──────────────▶ limited (LimitedUntil = the
//	   │                                          published reset, else now +
//	   │                                          default_cooldown_minutes)
//	   └─api_retry rate_limit/overloaded ─▶ warning ──3 within 5 min──▶ limited
//	limited ──LimitedUntil passed (tick or dispatch)──▶ probing
//	probing ──ONE dispatch reserves ProbeIssue; every other issue on the key
//	          stays backend_limited──▶ probe exits: succeeded / input_required
//	          → healthy (entry deleted); quota limit → limited again; any
//	          other exit keeps probing and the reservation is freed once the
//	          probe is neither running nor retrying.
//
// Only the event loop reads or writes these entries. The dispatch gate
// (gateDispatch) runs after the CORE-115 resolver has chosen (backend, host)
// and before a worker starts, in every admission path: tick dispatch and
// fireRetries (dispatch), reviewer dispatch, automation runs and pending
// input resumes. A refused issue records a BackendHold, which is what the
// backend_limited ineligible reason reads.

// Backend health statuses (CORE-053).
const (
	BackendStatusHealthy = "healthy"
	BackendStatusWarning = "warning"
	BackendStatusLimited = "limited"
	BackendStatusProbing = "probing"
)

const (
	// backendThrottleWindow / backendThrottleThreshold: this many advisory
	// api_retry rate_limit/overloaded signals on one key within the window
	// open the breaker for a throttle cooldown. Fewer only warn.
	backendThrottleWindow    = 5 * time.Minute
	backendThrottleThreshold = 3
	// backendThrottleCooldown is the minimum time a throttle-opened breaker
	// stays limited (the vendor's retry delay is used when longer, up to
	// backendThrottleMaxCooldown).
	backendThrottleCooldown    = 5 * time.Minute
	backendThrottleMaxCooldown = time.Hour
)

// Upper bounds on how far ahead a breaker may hold a backend (M3-close V1 /
// BH-M3-3). Without them a misparsed or hostile reset ("resetsAt" in 2100,
// retry_delay_ms 1e12) parked a whole (backend, host) for years, was
// persisted, and — because a held retry is rescheduled for the breaker's
// Until — defeated the 6h retry cap. The bound depends on how trustworthy
// the reset is:
//
//   - structured rate_limit_event, five_hour window: 5h15m (the window is 5
//     hours; 15 min of clock/jitter slack);
//   - structured, seven_day* window: 7d1h (a weekly limit legitimately
//     resets up to 7 days out);
//   - structured, any other or unknown window (overage, a new type): 24h;
//   - a reset read from TEXT (Codex, or a Claude result's "resets 3pm"):
//     6h, the same as rateLimitVendorDelayCap. Text resets are zone-less or
//     locale-formatted display strings; a wrong parse must heal within a
//     working session, and the cost of waking early is one probe run that
//     re-opens the breaker with a fresh parse;
//   - unknown reset: the configured cooldown, itself capped at 24h.
//
// An operator can always clear a breaker early (ClearBackendBreaker).
const (
	backendCapFiveHour  = 5*time.Hour + 15*time.Minute
	backendCapSevenDay  = 7*24*time.Hour + time.Hour
	backendCapStructure = 24 * time.Hour
	backendCapText      = rateLimitVendorDelayCap
)

// backendLimitCap is the longest a breaker opened by (kind, source,
// limitType) may stay limited from now.
func backendLimitCap(kind, source, limitType string) time.Duration {
	if kind == string(agent.LimitKindThrottle) {
		return backendThrottleMaxCooldown
	}
	switch source {
	case agent.LimitSourceRateLimitEvent:
		switch {
		case limitType == "five_hour":
			return backendCapFiveHour
		case strings.HasPrefix(limitType, "seven_day"):
			return backendCapSevenDay
		default:
			return backendCapStructure
		}
	case agent.LimitSourceText, agent.LimitSourceResult:
		return backendCapText
	default:
		return backendCapStructure
	}
}

// clampLimitedUntil bounds until to now + the cap for the entry's origin.
func clampLimitedUntil(until, now time.Time, kind, source, limitType string) time.Time {
	if limit := now.Add(backendLimitCap(kind, source, limitType)); until.After(limit) {
		return limit
	}
	return until
}

// BackendHealthEntry is one (backend, worker host) circuit breaker. A value
// type with no reference fields, so maps.Clone is a deep copy.
type BackendHealthEntry struct {
	Backend string
	Host    string
	// Status is warning, limited or probing (healthy entries are deleted).
	Status string
	// Kind is the agent.LimitKind that opened the breaker (quota|throttle).
	Kind string
	// LimitType is the vendor window (five_hour, seven_day, ...), if known.
	LimitType string
	// Source is the LimitSignal source that opened the breaker.
	Source string
	// LimitedUntil is when the breaker half-opens: the vendor's published
	// reset when ResetKnown, else a cooldown end.
	LimitedUntil time.Time
	// ResetKnown is true when LimitedUntil is a vendor-published reset.
	ResetKnown bool
	// Since is when the breaker last left healthy.
	Since time.Time
	// Hits counts limit signals recorded since the breaker last closed.
	Hits int
	// LastIssue is the identifier whose run last reported a limit.
	LastIssue string
	// ProbeIssue is the identifier holding the single half-open probe.
	ProbeIssue string
	// ThrottleCount counts api_retry throttles in the current window, which
	// started at ThrottleWindowStart.
	ThrottleCount       int
	ThrottleWindowStart time.Time
}

// BackendHold records that the dispatch gate refused an issue: Key is the
// breaker that will admit it first and Until when (a past Until means "at
// the next dispatch attempt", e.g. while another issue holds the probe).
type BackendHold struct {
	Key   string
	Until time.Time
}

// Auto-switch sources (CORE-055).
const (
	// AutoSwitchSourceAutomation — a rate_limited automation rule switched.
	AutoSwitchSourceAutomation = "automation"
	// AutoSwitchSourceBackendFallback — agent.backend_fallback rerouted.
	AutoSwitchSourceBackendFallback = "backend_fallback"
)

// AutoSwitchRecord is the provenance of an automatic per-issue override,
// captured at the switch and persisted with it (CORE-055). It describes the
// override for the NEXT dispatch, not the running session.
type AutoSwitchRecord struct {
	Source      string
	FromBackend string
	FromProfile string
	ToBackend   string
	ToProfile   string
	Reason      string
	// FromKey is the breaker key the issue was moved away from; switch_back
	// at_reset waits for it (CORE-054).
	FromKey    string
	SwitchedAt time.Time
}

// BackendHealthKey is the breaker key for a backend on a worker host.
func BackendHealthKey(backend, host string) string {
	if host == "" {
		return backend
	}
	return backend + "@" + host
}

// backendCooldown is the time a breaker stays limited when the vendor did
// not publish a reset: agent.backend_fallback.default_cooldown_minutes, 15
// minutes by default. BackendFallback is read-only after startup (not in the
// cfgMu allowlist), so no lock is needed.
func (o *Orchestrator) backendCooldown() time.Duration {
	return time.Duration(o.cfg.Agent.BackendFallback.CooldownMinutes()) * time.Minute
}

// recordBackendLimit opens (or extends) the breaker for (backend, host) on a
// quota limit signal. A breaker is never shortened by a later signal with an
// earlier reset. Returns whether the entry changed. Event loop only.
func (o *Orchestrator) recordBackendLimit(state *State, backend, host, identifier string, sig *agent.LimitSignal, now time.Time) bool {
	if backend == "" || !sig.Terminal() {
		return false
	}
	if state.BackendHealth == nil {
		state.BackendHealth = make(map[string]BackendHealthEntry)
	}
	key := BackendHealthKey(backend, host)
	e := state.BackendHealth[key]
	wasOpen := e.Status == BackendStatusLimited || e.Status == BackendStatusProbing
	until, known := sig.ResetsAt, true
	if until.IsZero() || !until.After(now) {
		until, known = now.Add(min(o.backendCooldown(), backendCapStructure)), false
	}
	if capped := clampLimitedUntil(until, now, string(sig.Kind), sig.Source, sig.LimitType); !capped.Equal(until) {
		o.logger().Warn("orchestrator: backend reset too far ahead, capped",
			"backend", backend, "worker_host", host, "reset", until.UTC().Format(time.RFC3339),
			"capped_to", capped.UTC().Format(time.RFC3339), "source", sig.Source, "limit_type", sig.LimitType)
		until = capped
	}
	if e.Status == BackendStatusLimited && e.LimitedUntil.After(until) {
		until, known = e.LimitedUntil, e.ResetKnown
	}
	e.Backend, e.Host = backend, host
	e.Status = BackendStatusLimited
	e.Kind = string(sig.Kind)
	e.LimitType = sig.LimitType
	e.Source = sig.Source
	e.LimitedUntil, e.ResetKnown = until, known
	if !wasOpen || e.Since.IsZero() {
		e.Since = now
	}
	e.Hits++
	e.LastIssue = identifier
	e.ProbeIssue = ""
	state.BackendHealth[key] = e
	o.logger().Warn("orchestrator: backend limited, breaker open",
		"backend", backend, "worker_host", host, "identifier", identifier,
		"limited_until", until.UTC().Format(time.RFC3339), "reset_known", known,
		"limit_type", sig.LimitType, "source", sig.Source)
	return true
}

// recordBackendThrottle counts an advisory api_retry rate_limit/overloaded
// signal (CORE-050) against (backend, host). backendThrottleThreshold of
// them within backendThrottleWindow open the breaker for
// max(backendThrottleCooldown, the vendor's retry delay); fewer mark it
// warning, which never gates dispatch. Returns whether the status changed.
func (o *Orchestrator) recordBackendThrottle(state *State, backend, host, identifier string, sig *agent.LimitSignal, now time.Time) bool {
	if backend == "" || sig == nil || sig.Kind != agent.LimitKindThrottle || sig.Source != agent.LimitSourceAPIRetry {
		return false
	}
	if state.BackendHealth == nil {
		state.BackendHealth = make(map[string]BackendHealthEntry)
	}
	key := BackendHealthKey(backend, host)
	e := state.BackendHealth[key]
	if e.ThrottleWindowStart.IsZero() || now.Sub(e.ThrottleWindowStart) > backendThrottleWindow {
		e.ThrottleWindowStart, e.ThrottleCount = now, 0
	}
	e.ThrottleCount++
	e.Backend, e.Host = backend, host
	changed := false
	switch e.Status {
	case BackendStatusLimited, BackendStatusProbing:
		// Already gating; the count only matters once it closes.
	default:
		if e.ThrottleCount >= backendThrottleThreshold {
			e.Status = BackendStatusLimited
			e.Kind = string(agent.LimitKindThrottle)
			e.LimitType = ""
			e.Source = sig.Source
			e.LimitedUntil = now.Add(min(max(backendThrottleCooldown, sig.RetryAfter), backendThrottleMaxCooldown))
			e.ResetKnown = false
			e.Since = now
			e.Hits++
			e.ProbeIssue = ""
			o.logger().Warn("orchestrator: repeated api_retry rate limits, breaker open",
				"backend", backend, "worker_host", host, "identifier", identifier,
				"retries_in_window", e.ThrottleCount, "error", sig.ErrorCategory,
				"limited_until", e.LimitedUntil.UTC().Format(time.RFC3339))
		} else if e.Status != BackendStatusWarning {
			e.Status = BackendStatusWarning
			e.Since = now
		}
		changed = e.Status != state.BackendHealth[key].Status
	}
	e.LastIssue = identifier
	state.BackendHealth[key] = e
	return changed
}

// advanceBackendHealth runs once per tick: an expired limited breaker
// half-opens (probing, no reservation), a probe reservation whose issue is
// neither running, retrying nor resuming is freed, and a warning whose
// throttle window has passed is dropped. Returns whether anything changed.
func advanceBackendHealth(state *State, now time.Time) bool {
	if len(state.BackendHealth) == 0 {
		return false
	}
	live := make(map[string]struct{}, len(state.Running)+len(state.RetryAttempts))
	for _, r := range state.Running {
		live[r.Issue.Identifier] = struct{}{}
	}
	for _, r := range state.RetryAttempts {
		live[r.Identifier] = struct{}{}
	}
	for ident := range state.PendingInputResumes {
		live[ident] = struct{}{}
	}
	changed := false
	for key, e := range state.BackendHealth {
		switch e.Status {
		case BackendStatusLimited:
			if !now.Before(e.LimitedUntil) {
				e.Status, e.ProbeIssue = BackendStatusProbing, ""
				changed = true
				slog.Info("orchestrator: backend limit expired, half-open probe allowed", "breaker", key)
			}
		case BackendStatusProbing:
			if _, ok := live[e.ProbeIssue]; e.ProbeIssue != "" && !ok {
				e.ProbeIssue = ""
				changed = true
			}
		case BackendStatusWarning:
			if now.Sub(e.ThrottleWindowStart) > backendThrottleWindow {
				delete(state.BackendHealth, key)
				changed = true
				continue
			}
		}
		state.BackendHealth[key] = e
	}
	return changed
}

// backendKeyBlocked reports whether key refuses identifier now, and until
// when (see BackendHold.Until). An expired limited breaker does not block:
// the caller may take the probe.
func backendKeyBlocked(state State, key, identifier string, now time.Time) (bool, time.Time) {
	e, ok := state.BackendHealth[key]
	if !ok {
		return false, time.Time{}
	}
	switch e.Status {
	case BackendStatusLimited:
		if now.Before(e.LimitedUntil) {
			return true, e.LimitedUntil
		}
	case BackendStatusProbing:
		if e.ProbeIssue != "" && e.ProbeIssue != identifier {
			return true, e.LimitedUntil
		}
	}
	return false, time.Time{}
}

// admitBackendKey admits identifier on key, reserving the half-open probe
// when the breaker is (or just became) probing. Returns false when blocked.
func admitBackendKey(state *State, key, identifier string, now time.Time) bool {
	if blocked, _ := backendKeyBlocked(*state, key, identifier, now); blocked {
		return false
	}
	e, ok := state.BackendHealth[key]
	if !ok {
		return true
	}
	switch e.Status {
	case BackendStatusLimited: // expired: this dispatch is the probe
		e.Status, e.ProbeIssue = BackendStatusProbing, identifier
	case BackendStatusProbing:
		if e.ProbeIssue == "" {
			e.ProbeIssue = identifier
		}
	default:
		return true
	}
	state.BackendHealth[key] = e
	slog.Info("orchestrator: half-open probe dispatched", "breaker", key, "identifier", identifier)
	return true
}

// settleBackendProbe closes the breaker when the exited run was its probe
// and the backend served it (succeeded, or stopped to ask a question).
// Returns whether the breaker closed.
func settleBackendProbe(state *State, liveEntry *RunEntry, identifier string, reason TerminalReason) bool {
	if liveEntry == nil || (reason != TerminalSucceeded && reason != TerminalInputRequired) {
		return false
	}
	key := BackendHealthKey(liveEntry.Backend, liveEntry.WorkerHost)
	e, ok := state.BackendHealth[key]
	if !ok || e.Status != BackendStatusProbing || e.ProbeIssue != identifier {
		return false
	}
	delete(state.BackendHealth, key)
	slog.Info("orchestrator: backend probe succeeded, breaker closed", "breaker", key, "identifier", identifier)
	return true
}

// setBackendHold / clearBackendHold maintain State.BackendLimitedHolds.
func setBackendHold(state *State, identifier string, hold BackendHold) {
	if state.BackendLimitedHolds == nil {
		state.BackendLimitedHolds = make(map[string]BackendHold)
	}
	state.BackendLimitedHolds[identifier] = hold
}

func clearBackendHold(state *State, identifier string) {
	delete(state.BackendLimitedHolds, identifier)
}

// BackendHoldActive is backendHoldActive for snapshot readers (cmd/itervox).
func BackendHoldActive(state State, identifier string) bool {
	return backendHoldActive(state, identifier)
}

// backendHoldActive reports whether identifier's recorded hold still
// applies: its breaker is limited, or probing with another issue's probe.
// Clock-free on purpose — ineligibleReasonShared has no "now"; expiry is
// applied by advanceBackendHealth at the tick and by the gate itself.
func backendHoldActive(state State, identifier string) bool {
	h, ok := state.BackendLimitedHolds[identifier]
	if !ok {
		return false
	}
	e, ok := state.BackendHealth[h.Key]
	if !ok {
		return false
	}
	switch e.Status {
	case BackendStatusLimited:
		return true
	case BackendStatusProbing:
		return e.ProbeIssue != "" && e.ProbeIssue != identifier
	}
	return false
}

// holdRetryDelay converts a hold into a retry delay: until its Until, and
// at least one poll interval when that is already past (a probe held by
// another issue is re-checked on a later tick, never in a tight loop).
func holdRetryDelay(state State, hold BackendHold, now time.Time) int {
	floor := time.Duration(max(state.PollIntervalMs, 1000)) * time.Millisecond
	// Never past the 6h retry cap (V1): the retry wakes, the gate holds it
	// again if the breaker is still open, and it is rescheduled.
	d := min(max(hold.Until.Sub(now), floor), rateLimitVendorDelayCap)
	return int(d / time.Millisecond)
}

// describeHold renders a hold for logs.
func describeHold(h BackendHold) string {
	if h.Until.IsZero() {
		return h.Key
	}
	return fmt.Sprintf("%s until %s", h.Key, h.Until.UTC().Format(time.RFC3339))
}

// logDispatchHeld logs a backend hold at Info when it is new or changed for
// identifier, and at Debug while it stays the same (CORE-173 b): the tick
// re-evaluates every hold through the gate, so an unchanged hold would
// otherwise repeat its Info line every poll interval. Event loop only.
func (o *Orchestrator) logDispatchHeld(identifier string, hold BackendHold) {
	if prev, ok := o.loggedHolds[identifier]; ok && prev.Key == hold.Key && prev.Until.Equal(hold.Until) {
		o.logger().Debug("orchestrator: dispatch still held, backend limited",
			"identifier", identifier, "hold", describeHold(hold))
		return
	}
	if o.loggedHolds == nil {
		o.loggedHolds = make(map[string]BackendHold)
	}
	o.loggedHolds[identifier] = hold
	o.logger().Info("orchestrator: dispatch held, backend limited",
		"identifier", identifier, "hold", describeHold(hold))
}

// forgetLoggedHolds drops the log-dedupe entry of every identifier that is
// no longer held, so a later hold is logged at Info again. Event loop only.
func (o *Orchestrator) forgetLoggedHolds(state State) {
	for id := range o.loggedHolds {
		if _, held := state.BackendLimitedHolds[id]; !held {
			delete(o.loggedHolds, id)
		}
	}
}
