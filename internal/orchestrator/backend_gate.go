package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-053/054 — the dispatch gate and declarative backend fallback.
//
// Precedence (decided here and in handleFailedExit, documented in
// docs/configuration.md):
//
//  1. An operator per-issue backend pin wins. A pinned issue is never
//     rerouted by backend_fallback: when its pinned backend is limited it is
//     held with backend_limited.
//  2. agent.backend_fallback, for profiles it maps (profile_map, or
//     on_unmapped: backend_hint): a limited target is rerouted to the next
//     healthy backend of the chain, and a TerminalRateLimited exit of such a
//     run is handled by the fallback alone — the rate_limited automations
//     are not evaluated for that exit.
//  3. rate_limited automations for everything else (unmapped profiles,
//     pinned issues, retry exhaustion classified from text), unchanged.
//
// Every fallback switch of an implementer run counts against the per-issue
// switch cap (agent.max_switches_per_issue_per_window); with the cap spent
// the issue is held instead of rerouted. Reviewer and automation reroutes
// are per-run and write no per-issue override, so they are not counted.

// gateRequest is one admission through the gate.
type gateRequest struct {
	identifier  string
	in          dispatchTargetInput
	profileName string
	host        string
	hosts       []string
	// allowReroute enables backend_fallback for this dispatch.
	allowReroute bool
	// rerouteAllowed, when set, is consulted once before the first reroute
	// (the switch cap for implementer runs).
	rerouteAllowed func() bool
}

// gateResult is the admitted target, or a hold.
type gateResult struct {
	target      dispatchTarget
	profileName string
	host        string
	rerouted    bool
	fromBackend string
	fromProfile string
	fromKey     string
	held        bool
	hold        BackendHold
}

// hostCandidates is host first, then every other configured host.
func hostCandidates(host string, hosts []string) []string {
	out := []string{host}
	for _, h := range hosts {
		if h != host {
			out = append(out, h)
		}
	}
	return out
}

// gateDispatch applies the backend circuit breaker to a resolved target.
// Event loop only.
func (o *Orchestrator) gateDispatch(state *State, req gateRequest, now time.Time) gateResult {
	t := resolveDispatchTarget(req.in)
	res := gateResult{target: t, profileName: req.profileName, host: req.host}
	var hold BackendHold
	consider := func(key string) {
		_, until := backendKeyBlocked(*state, key, req.identifier, now)
		if hold.Key == "" || until.Before(hold.Until) {
			hold = BackendHold{Key: key, Until: until}
		}
	}
	admit := func(backend string) (string, bool) {
		for _, h := range hostCandidates(req.host, req.hosts) {
			key := BackendHealthKey(backend, h)
			if admitBackendKey(state, key, req.identifier, now) {
				return h, true
			}
			consider(key)
		}
		return "", false
	}
	if h, ok := admit(t.Backend); ok {
		res.host = h
		clearBackendHold(state, req.identifier)
		return res
	}
	fromKey := BackendHealthKey(t.Backend, req.host)
	fb := o.cfg.Agent.BackendFallback
	if req.allowReroute && fb.Enabled {
		allowed := req.rerouteAllowed == nil
		asked := false
		for _, b := range fb.Chain {
			if b == t.Backend {
				continue
			}
			alt, altProfile, ok := o.fallbackTarget(req, b)
			if !ok {
				continue
			}
			if !asked && !allowed {
				asked, allowed = true, req.rerouteAllowed()
				if !allowed {
					slog.Warn("orchestrator: backend_fallback skipped, switch cap reached",
						"identifier", req.identifier, "from", t.Backend, "to", b)
				}
			}
			if !allowed {
				break
			}
			if h, ok := admit(b); ok {
				clearBackendHold(state, req.identifier)
				return gateResult{
					target: alt, profileName: altProfile, host: h, rerouted: true,
					fromBackend: t.Backend, fromProfile: req.profileName, fromKey: fromKey,
				}
			}
		}
	}
	res.held = true
	res.hold = hold
	setBackendHold(state, req.identifier, hold)
	return res
}

// fallbackTarget resolves the dispatch target for req on backend b through
// agent.backend_fallback: the profile_map counterpart of the issue's profile
// (whose own command runs), else — with on_unmapped: backend_hint — the
// issue's own command hinted to b when the resolver accepts that (wrappers
// only, CORE-115). ok is false when b cannot serve the issue.
func (o *Orchestrator) fallbackTarget(req gateRequest, b string) (dispatchTarget, string, bool) {
	fb := o.cfg.Agent.BackendFallback
	name, mapped := o.fallbackProfileFor(req.profileName, b, req.in.DefaultCommand)
	if mapped {
		in := dispatchTargetInput{DefaultCommand: req.in.DefaultCommand, DefaultBackend: req.in.DefaultBackend}
		if name != "" {
			o.cfgMu.RLock()
			profile, ok := o.cfg.Agent.Profiles[name]
			o.cfgMu.RUnlock()
			if !ok || !config.ProfileEnabled(profile) {
				slog.Warn("orchestrator: backend_fallback target profile unavailable",
					"identifier", req.identifier, "profile", name, "backend", b)
				return dispatchTarget{}, "", false
			}
			in.Profile = &profile
		}
		alt := resolveDispatchTarget(in)
		if alt.Backend != b {
			return dispatchTarget{}, "", false
		}
		return alt, name, true
	}
	if fb.OnUnmapped == config.BackendFallbackUnmappedBackendHint {
		in := req.in
		in.IssueBackend, in.RecoveryBackend = b, ""
		alt := resolveDispatchTarget(in)
		if alt.Backend != b || alt.Reason != "" {
			return dispatchTarget{}, "", false
		}
		return alt, req.profileName, true
	}
	return dispatchTarget{}, "", false
}

// fallbackProfileFor returns the profile an issue running profile (""=the
// default command) uses on backend b, and whether one is mapped. A direct
// profile_map entry wins; otherwise a profile that is itself a mapping
// target resolves through its source row, so {coder: {codex: coder-codex}}
// also sends coder-codex back to coder on claude. name "" with mapped=true
// means the default command.
func (o *Orchestrator) fallbackProfileFor(profile, b, defaultCommand string) (string, bool) {
	fb := o.cfg.Agent.BackendFallback
	if name := fb.Lookup(profile, b); name != "" {
		return o.profileKeyToName(name), true
	}
	sources := make([]string, 0, len(fb.ProfileMap))
	for src := range fb.ProfileMap {
		sources = append(sources, src)
	}
	slices.Sort(sources)
	for _, src := range sources {
		row := fb.ProfileMap[src]
		isTarget := false
		for _, target := range row {
			if target != "" && target == profile {
				isTarget = true
				break
			}
		}
		if !isTarget {
			continue
		}
		if o.profileKeyBackend(src, defaultCommand) == b {
			return o.profileKeyToName(src), true
		}
		if name := row[b]; name != "" && name != profile {
			return o.profileKeyToName(name), true
		}
	}
	return "", false
}

// profileKeyToName maps the profile_map "default" key to "" (no profile,
// agent.command) unless a profile is literally named "default".
func (o *Orchestrator) profileKeyToName(key string) string {
	if key != config.BackendFallbackDefaultProfileKey {
		return key
	}
	o.cfgMu.RLock()
	_, named := o.cfg.Agent.Profiles[key]
	o.cfgMu.RUnlock()
	if named {
		return key
	}
	return ""
}

// profileKeyBackend is the backend a profile_map source key runs on.
func (o *Orchestrator) profileKeyBackend(key, defaultCommand string) string {
	o.cfgMu.RLock()
	profile, ok := o.cfg.Agent.Profiles[key]
	o.cfgMu.RUnlock()
	if !ok {
		if key == config.BackendFallbackDefaultProfileKey {
			return config.BackendFromCommand(defaultCommand)
		}
		return ""
	}
	return config.ProfileRunsBackend(profile, defaultCommand)
}

// operatorPinnedBackend is the operator's per-issue backend pin (the
// SetIssueBackend side map), "" when none.
func (o *Orchestrator) operatorPinnedBackend(identifier string) string {
	o.issueBackendsMu.RLock()
	defer o.issueBackendsMu.RUnlock()
	return o.issueBackends[identifier]
}

// isReviewerInjected reports whether the issue's next dispatch runs a
// reviewer profile (reviewer dispatch installs the marker; retries keep it).
func (o *Orchestrator) isReviewerInjected(identifier string) bool {
	o.issueProfilesMu.RLock()
	defer o.issueProfilesMu.RUnlock()
	_, ok := o.reviewerInjectedProfiles[identifier]
	return ok
}

// backendFallbackCovers reports whether agent.backend_fallback, not the
// rate_limited automations, handles a limited run of identifier on
// (profile, backend): fallback enabled, no operator pin, and some other
// chain backend has a counterpart (or on_unmapped: backend_hint).
func (o *Orchestrator) backendFallbackCovers(identifier, profile, backend string) bool {
	fb := o.cfg.Agent.BackendFallback
	if !fb.Enabled || o.operatorPinnedBackend(identifier) != "" {
		return false
	}
	if fb.OnUnmapped == config.BackendFallbackUnmappedBackendHint {
		return true
	}
	o.cfgMu.RLock()
	defaultCommand := o.cfg.Agent.Command
	o.cfgMu.RUnlock()
	for _, b := range fb.Chain {
		if b == backend {
			continue
		}
		if _, ok := o.fallbackProfileFor(profile, b, defaultCommand); ok {
			return true
		}
	}
	return false
}

// recordFallbackSwitch installs the sticky per-issue override for an
// implementer run rerouted by backend_fallback, with its provenance, counts
// it against the switch cap, persists, and posts one tracker comment.
func (o *Orchestrator) recordFallbackSwitch(state *State, issue domain.Issue, g gateResult, now time.Time) {
	ident := issue.Identifier
	if g.profileName == "" {
		delete(state.IssueProfiles, ident)
	} else {
		state.IssueProfiles[ident] = g.profileName
	}
	state.IssueBackends[ident] = g.target.Backend
	state.AutoSwitchedIdentifiers[ident] = struct{}{}
	state.AutoSwitchedAt[ident] = now
	reason := fmt.Sprintf("backend_fallback: %s limited", g.fromKey)
	if e, ok := state.BackendHealth[g.fromKey]; ok && !e.LimitedUntil.IsZero() {
		reason += " until " + e.LimitedUntil.UTC().Format(time.RFC3339)
		if !e.ResetKnown {
			reason += " (cooldown; reset unknown)"
		}
	}
	if state.AutoSwitchInfo == nil {
		state.AutoSwitchInfo = make(map[string]AutoSwitchRecord)
	}
	state.AutoSwitchInfo[ident] = AutoSwitchRecord{
		Source:      AutoSwitchSourceBackendFallback,
		FromBackend: g.fromBackend, FromProfile: g.fromProfile,
		ToBackend: g.target.Backend, ToProfile: g.profileName,
		Reason: reason, FromKey: g.fromKey, SwitchedAt: now,
	}
	o.recordRateLimitSwitch(state, issue.ID, now)
	o.saveAutoSwitchedToDisk(state)
	o.logger().Warn("orchestrator: backend_fallback switched issue",
		"identifier", ident, "from_backend", g.fromBackend, "to_backend", g.target.Backend,
		"from_profile", g.fromProfile, "to_profile", g.profileName, "reason", reason)
	if o.logBuf != nil {
		o.logBuf.Add(ident, makeBufLine("WARN", fmt.Sprintf("orchestrator: backend_fallback %s → %s (%s)",
			fallbackProfileLabel(g.fromProfile)+"@"+g.fromBackend, fallbackProfileLabel(g.profileName)+"@"+g.target.Backend, reason)))
	}
	body := fmt.Sprintf("🤖 Itervox: backend fallback\n\n"+
		"Backend `%s` is limited (%s). This issue now runs profile **%s** on `%s`; it switches back per `agent.backend_fallback.switch_back`.",
		g.fromBackend, reason, fallbackProfileLabel(g.profileName), g.target.Backend)
	goSafe(&o.commentWg, "backend-fallback-comment", ident, func() {
		o.postManagedComment(issue, body)
	}, o.withPanicFailure("backend-fallback-comment", ident, nil))
}

// clearAutoSwitch drops an automatic override and its provenance.
func clearAutoSwitch(state *State, identifier string) {
	delete(state.IssueProfiles, identifier)
	delete(state.IssueBackends, identifier)
	delete(state.AutoSwitchedIdentifiers, identifier)
	delete(state.AutoSwitchedAt, identifier)
	delete(state.AutoSwitchInfo, identifier)
}

// clearAutoSwitchOnSuccess decides whether a successful run clears the
// issue's automatic override. rate_limited automation switches keep the
// historical clear-on-success (Gap §1.3). backend_fallback switches follow
// switch_back: on_success clears once min_dwell_minutes have passed;
// at_reset and manual never clear on success (that clear is what made the
// next dispatch flap back onto the still-limited backend).
func (o *Orchestrator) clearAutoSwitchOnSuccess(state State, identifier string, now time.Time) bool {
	rec, ok := state.AutoSwitchInfo[identifier]
	if !ok || rec.Source != AutoSwitchSourceBackendFallback {
		return true
	}
	fb := o.cfg.Agent.BackendFallback
	if fb.SwitchBack != config.BackendFallbackSwitchBackOnSuccess {
		return false
	}
	return !now.Before(rec.SwitchedAt.Add(time.Duration(fb.MinDwellMinutes) * time.Minute))
}

// revertBackendFallbackSwitches implements switch_back: at_reset. Once an
// issue has dwelt min_dwell_minutes on the fallback AND the breaker it left
// is no longer limited (its published reset, or the cooldown, has passed),
// the override is cleared so the next dispatch returns to the natural
// profile — through the gate, so a half-open breaker admits it as the
// probe or holds it. Runs on the tick. Returns how many were reverted.
func (o *Orchestrator) revertBackendFallbackSwitches(state *State, now time.Time) int {
	fb := o.cfg.Agent.BackendFallback
	if !fb.Enabled || fb.SwitchBack != config.BackendFallbackSwitchBackAtReset || len(state.AutoSwitchInfo) == 0 {
		return 0
	}
	dwell := time.Duration(fb.MinDwellMinutes) * time.Minute
	reverted := 0
	for ident, rec := range state.AutoSwitchInfo {
		if rec.Source != AutoSwitchSourceBackendFallback || now.Before(rec.SwitchedAt.Add(dwell)) {
			continue
		}
		if e, ok := state.BackendHealth[rec.FromKey]; ok && e.Status == BackendStatusLimited && now.Before(e.LimitedUntil) {
			continue
		}
		clearAutoSwitch(state, ident)
		reverted++
		slog.Info("orchestrator: backend_fallback switched back at reset",
			"identifier", ident, "to_backend", rec.FromBackend, "from_backend", rec.ToBackend)
	}
	if reverted > 0 {
		o.saveAutoSwitchedToDisk(state)
	}
	return reverted
}

// postManagedComment posts a managed tracker comment; fire-and-forget
// callers run it on commentWg. Bounded by a 15s timeout.
func (o *Orchestrator) postManagedComment(issue domain.Issue, body string) {
	if o == nil || o.tracker == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := o.writeSink().CreateComment(ctx, issue.ID, issue.Identifier, tracker.MarkManagedComment(body)); err != nil {
		slog.Warn("orchestrator: failed to post comment", "identifier", issue.Identifier, "error", err)
	}
}

// CheckIssueBackendPin reports why pinning identifier to backend would be
// refused at dispatch (CORE-056): the pin goes through the same resolver
// (CORE-115), so a codex pin over a "claude ..." command (or the reverse)
// is an error carrying the resolver's reason. Safe from any goroutine: it
// reads the published snapshot and cfg under cfgMu, never live State.
func (o *Orchestrator) CheckIssueBackendPin(identifier, backend string) error {
	if backend == "" {
		return nil
	}
	snap := o.Snapshot()
	profileName := snap.IssueProfiles[identifier]
	o.cfgMu.RLock()
	in := dispatchTargetInput{
		DefaultCommand: o.cfg.Agent.Command,
		DefaultBackend: o.cfg.Agent.Backend,
		IssueBackend:   backend,
	}
	if profile, ok := o.cfg.Agent.Profiles[profileName]; ok && profileName != "" && config.ProfileEnabled(profile) {
		in.Profile = &profile
	}
	o.cfgMu.RUnlock()
	if t := resolveDispatchTarget(in); t.Reason != "" {
		return fmt.Errorf("orchestrator: %s", t.Reason)
	}
	return nil
}

// BackendLimitedForProfile reports whether the LOCAL breaker (CORE-053) of
// the backend that profileKey runs on refuses new work now, and until when
// (CORE-173 a). It is for agent runs outside the dispatch gate — the
// dependency analyzer — so it reads the published snapshot, never State,
// and takes no probe: an expired breaker does not refuse. Safe from any
// goroutine; must not be called with cfgMu held (Snapshot rule).
func (o *Orchestrator) BackendLimitedForProfile(profileKey string, now time.Time) (backend string, until time.Time, limited bool) {
	backend = o.profileKeyBackend(profileKey, o.cfg.Agent.Command)
	if backend == "" {
		return "", time.Time{}, false
	}
	blocked, until := backendKeyBlocked(o.Snapshot(), BackendHealthKey(backend, ""), "", now)
	return backend, until, blocked
}
