package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// dependencyRefreshTarget is one row the off-loop refresher must fetch. Key is
// the DependencyAudit map key at selection time; it can differ from the key
// the fetched issue hashes to, which the result handler reconciles.
type dependencyRefreshTarget struct {
	Key        string
	IssueID    string
	Identifier string
}

// selectDependencyRefreshBatch picks the next batch of dependency-audit rows to
// refresh and arms their in-flight latch. Pure with respect to I/O — it runs on
// the event-loop goroutine and mutates only State, which is legal there.
//
// Ordering is unchanged from the former inline implementation: rows with a
// queued blockers_resolved consumer first (they cannot fire until the audit row
// reflects post-unblock state), then oldest LastAuditedAt, then key for
// determinism.
func selectDependencyRefreshBatch(
	state *State,
	now time.Time,
	interval time.Duration,
	batchSize int,
) []dependencyRefreshTarget {
	if len(state.DependencyAudit) == 0 || batchSize <= 0 {
		return nil
	}
	queuePriority := blockersResolvedQueueIdentifiers(state)

	type candidate struct {
		target    dependencyRefreshTarget
		priority  bool
		auditedAt time.Time
	}
	candidates := make([]candidate, 0, len(state.DependencyAudit))
	for key, entry := range state.DependencyAudit {
		if entry == nil {
			delete(state.DependencyAudit, key)
			continue
		}
		if entry.IssueID == "" && entry.Identifier == "" {
			// Unfetchable. The former fetchDependencyAuditIssue deleted these
			// in its default branch; do it here instead so the worker never
			// receives a target it cannot act on.
			delete(state.DependencyAudit, key)
			continue
		}
		if entry.InFlight {
			continue
		}
		// Gap E: the candidate loop (event_loop.go:118) already refreshed this
		// row THIS tick — auditFetchedIssueDependencies stamps LastAuditedAt
		// with the same `now` reconcileDependencyRefresh is called with a few
		// lines later. Without this skip every newly-seen candidate row is
		// immediately eligible (LastRefreshAttemptAt is unset the first time),
		// which is both a redundant FetchIssueDetail per new row per tick and,
		// combined with a slow batch, the enabler for a stale-snapshot re-audit
		// (Gap C). The deleted refreshKnownDependencyAudits had this same
		// check; selectDependencyRefreshBatch never got an equivalent.
		if entry.LastAuditedAt.Equal(now) {
			continue
		}
		if !entry.LastRefreshAttemptAt.IsZero() && now.Sub(entry.LastRefreshAttemptAt) < interval {
			continue
		}
		_, priority := queuePriority[entry.Identifier]
		if !priority && entry.IssueID != "" {
			_, priority = queuePriority[entry.IssueID]
		}
		candidates = append(candidates, candidate{
			target: dependencyRefreshTarget{
				Key:        key,
				IssueID:    entry.IssueID,
				Identifier: entry.Identifier,
			},
			priority:  priority,
			auditedAt: entry.LastAuditedAt,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority
		}
		if !candidates[i].auditedAt.Equal(candidates[j].auditedAt) {
			return candidates[i].auditedAt.Before(candidates[j].auditedAt)
		}
		return candidates[i].target.Key < candidates[j].target.Key
	})
	if len(candidates) > batchSize {
		candidates = candidates[:batchSize]
	}

	out := make([]dependencyRefreshTarget, 0, len(candidates))
	for _, c := range candidates {
		entry := state.DependencyAudit[c.target.Key]
		if entry == nil {
			continue
		}
		entry.InFlight = true
		entry.LastRefreshAttemptAt = now
		out = append(out, c.target)
	}
	return out
}

// dependencyRefreshSendTimeout bounds how long the worker waits to hand its
// result to the event loop. On expiry the result is dropped; the loop-side
// watchdog then releases the latch on a later tick. Matches the 100ms fallback
// used by the other worker-to-loop sends (see worker.go:1394-1401 — a
// goroutine-to-loop send with a time.After fallback, unlike reconcile.go's
// 100ms fallbacks, which are loop-to-loop SELF-sends).
const dependencyRefreshSendTimeout = 100 * time.Millisecond

// DependencyRefreshIssue pairs a fetched issue with the DependencyAudit key
// that was requested. The two can differ when an issue's identity changed
// between audits; the handler migrates the row.
type DependencyRefreshIssue struct {
	RequestKey string
	Issue      domain.Issue
}

// DependencyRefreshResult is what one off-loop batch reports back. It is a
// pure value — the worker never mutates State.
type DependencyRefreshResult struct {
	// BatchKeys is every key the batch held. The handler clears InFlight for
	// all of them unconditionally, so a partially-completed or panicked batch
	// cannot wedge rows.
	BatchKeys []string
	Issues    []DependencyRefreshIssue
	// MissingKeys are rows whose issue returned ErrNotFound — delete them.
	MissingKeys []string
	// FailedKeys are rows that hit a transient error — retain and count.
	FailedKeys             []string
	BlockersResolvedIssues []domain.Issue
	// BlockersResolvedRan reports whether the states fetch was attempted at
	// all; BlockersResolvedOK whether it succeeded. The seq watermark advances
	// only when both are true.
	BlockersResolvedRan bool
	BlockersResolvedOK  bool
	StartedAt           time.Time
	FinishedAt          time.Time
	// Generation identifies the batch. The handler drops any result whose
	// Generation no longer matches State.DepsRefreshGeneration.
	Generation int64
	// SeqAtLaunch is DependencyTransitionSeq as observed at launch, before the
	// fetch window opened. The watermark advances to THIS, not to the live
	// seq, so transitions recorded by other paths during the window are not
	// absorbed.
	SeqAtLaunch int64
}

// dependencyRefreshOrder is one immutable batch instruction handed to the
// off-loop worker. Passing a struct rather than positional parameters keeps
// the launch-time metadata (Generation, SeqAtLaunch) impossible to omit.
type dependencyRefreshOrder struct {
	Targets     []dependencyRefreshTarget
	States      []string
	Timeout     time.Duration
	StartedAt   time.Time
	Generation  int64
	SeqAtLaunch int64
}

// runDependencyRefresh performs one batch of dependency-audit tracker fetches
// off the event-loop goroutine and reports the result via the events channel.
//
// INVARIANT: this function must never read or write orchestrator State. It
// receives an immutable work order and returns data.
func (o *Orchestrator) runDependencyRefresh(
	ctx context.Context,
	order dependencyRefreshOrder,
) {
	defer o.depsRefreshWg.Done()

	result := &DependencyRefreshResult{
		StartedAt:   order.StartedAt,
		BatchKeys:   make([]string, 0, len(order.Targets)),
		Generation:  order.Generation,
		SeqAtLaunch: order.SeqAtLaunch,
	}
	for _, t := range order.Targets {
		result.BatchKeys = append(result.BatchKeys, t.Key)
	}

	defer func() {
		if r := recover(); r != nil {
			slog.Error("orchestrator: dependency refresh panicked",
				"panic", r, "batch", len(result.BatchKeys))
		}
		result.FinishedAt = time.Now()
		o.sendDependencyRefreshResult(result)
	}()

	fetchCtx, cancel := context.WithTimeout(ctx, order.Timeout)
	defer cancel()

	// IMPORTANT-2 (final review): run the blockers_resolved states scan
	// BEFORE the per-row target loop, not after. Both passes share one
	// order.Timeout budget; the deleted synchronous code
	// (auditBlockersResolvedAutomationSources, formerly event_loop.go:110)
	// ran the states scan first, ahead of refreshKnownDependencyAudits
	// (formerly event_loop.go:111). Running the target loop first let it
	// consume the ENTIRE budget on a board with enough non-candidate audit
	// rows (batch size 100 x the spec's own 300ms tracker p50 = 30 000ms,
	// exactly DefaultDependencyAuditRefreshTimeoutMs) and starve the states
	// scan every single batch: BlockersResolvedRan would never become true,
	// LastBlockersResolvedAuditSeq would never advance, and
	// pendingBlockersResolvedStates would keep returning the same states
	// forever — silently and permanently disabling every blockers_resolved
	// automation sourced from the states scan. Reordering restores the
	// synchronous code's priority with no new config knob.
	if len(order.States) > 0 && fetchCtx.Err() == nil {
		result.BlockersResolvedRan = true
		issues, err := o.tracker.FetchIssuesByStates(fetchCtx, order.States)
		if err != nil {
			slog.Warn("orchestrator: blockers_resolved states fetch failed", "error", err)
		} else {
			result.BlockersResolvedOK = true
			result.BlockersResolvedIssues = issues
		}
	}

	for _, t := range order.Targets {
		if fetchCtx.Err() != nil {
			// Remaining targets stay unreported; the handler clears their
			// InFlight via BatchKeys regardless.
			break
		}
		issue, err := o.fetchDependencyRefreshIssue(fetchCtx, t)
		switch {
		case errors.Is(err, tracker.ErrNotFound):
			result.MissingKeys = append(result.MissingKeys, t.Key)
		case err != nil:
			slog.Warn("orchestrator: dependency audit refresh failed",
				"identifier", t.Identifier, "issue_id", t.IssueID, "error", err)
			result.FailedKeys = append(result.FailedKeys, t.Key)
		default:
			result.Issues = append(result.Issues, DependencyRefreshIssue{
				RequestKey: t.Key,
				Issue:      *issue,
			})
		}
	}
}

func (o *Orchestrator) fetchDependencyRefreshIssue(
	ctx context.Context,
	t dependencyRefreshTarget,
) (*domain.Issue, error) {
	var (
		issue *domain.Issue
		err   error
	)
	switch {
	case t.IssueID != "":
		issue, err = o.tracker.FetchIssueDetail(ctx, t.IssueID)
	case t.Identifier != "":
		issue, err = o.tracker.FetchIssueByIdentifier(ctx, t.Identifier)
	default:
		return nil, tracker.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, tracker.ErrNotFound
	}
	return issue, nil
}

func (o *Orchestrator) sendDependencyRefreshResult(result *DependencyRefreshResult) {
	select {
	case o.events <- OrchestratorEvent{
		Type:              EventDependencyAuditRefreshed,
		DependencyRefresh: result,
	}:
	case <-time.After(dependencyRefreshSendTimeout):
		// Dropped. The worker cannot clear the latch itself — that would be a
		// State mutation from a worker goroutine. The loop-side watchdog in
		// reconcileDependencyRefresh releases it instead.
		slog.Warn("orchestrator: dependency refresh result send timed out",
			"batch", len(result.BatchKeys))
	}
}

// DependencyRefreshDegradedThreshold is how many back-to-back transient
// failures mark a row degraded on the snapshot. Exported because
// cmd/itervox/snapshot_rows.go will apply the same threshold when serialising
// rows (Task 7) — the two must not drift. Hard-coded until there is evidence
// anyone wants to tune it.
const DependencyRefreshDegradedThreshold = 3

// applyDependencyRefreshResult folds one off-loop batch back into State. Runs
// on the event-loop goroutine only.
//
// Order matters: for a non-nil result, the latch and per-row InFlight are
// cleared FIRST, so an early return or an unexpected shape further down can
// never leave rows wedged. A nil result is not a partially-completed batch —
// it means no batch was ever launched (the only producer always allocates a
// non-nil result before registering its deferred send), so it must NOT touch
// per-row InFlight for a batch that isn't this call's to clear; it only logs
// and returns.
func (o *Orchestrator) applyDependencyRefreshResult(
	ctx context.Context,
	state *State,
	result *DependencyRefreshResult,
	now time.Time,
) {
	if result == nil {
		slog.Warn("orchestrator: applyDependencyRefreshResult called with a nil result; ignoring")
		return
	}

	if result.Generation != state.DepsRefreshGeneration {
		slog.Warn("orchestrator: dropping stale dependency refresh result",
			"result_generation", result.Generation,
			"current_generation", state.DepsRefreshGeneration,
			"batch", len(result.BatchKeys))
		return
	}

	state.DepsRefreshInFlight = false
	state.DepsRefreshBatchSize = 0
	state.DepsRefreshStartedAt = time.Time{}
	if !result.FinishedAt.IsZero() && !result.StartedAt.IsZero() {
		state.DepsRefreshLastDurationMs = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
	}
	for _, key := range result.BatchKeys {
		if entry := state.DependencyAudit[key]; entry != nil {
			entry.InFlight = false
		}
	}

	for _, key := range result.MissingKeys {
		slog.Debug("orchestrator: dropping dependency audit row for missing issue", "key", key)
		delete(state.DependencyAudit, key)
	}

	for _, key := range result.FailedKeys {
		entry := state.DependencyAudit[key]
		if entry == nil {
			continue
		}
		entry.ConsecutiveFailures++
		if entry.ConsecutiveFailures == DependencyRefreshDegradedThreshold {
			slog.Warn("orchestrator: dependency audit row degraded",
				"identifier", entry.Identifier,
				"issue_id", entry.IssueID,
				"consecutive_failures", entry.ConsecutiveFailures)
		}
	}

	for _, refreshed := range result.Issues {
		// Gap C: drop results for rows pruned while the fetch was in flight
		// (terminal-state sweep, absent-from-tracker sweep) — resurrecting
		// them would undo a deliberate prune — AND drop results superseded by
		// a newer observation. Synchronously the fetch and the audit were the
		// same event-loop turn, so a newer snapshot could never land after an
		// older one. Asynchronously a batch can fetch issue X early, spend the
		// rest of the batch timeout on other rows, and land a stale snapshot
		// several ticks after the candidate loop already observed (and acted
		// on) a fresher state for the same issue. Applying the stale snapshot
		// would unconditionally re-arm auditIssueDependencies' WasBlocked
		// latch (dependency_audit.go) even though the real transition already
		// fired, producing a duplicate blockers_resolved dispatch — a
		// reopening of the v0.2.0 AUTO-1 finding.
		//
		// N-1 (Task 6 review round 2): use !Before (i.e. >=), not After
		// (i.e. strictly >). Several same-tick paths audit with the SAME
		// `now` this batch launched under but run AFTER the launch —
		// processPendingInputResumes and drainAutomationQueueWithCandidates
		// both call auditFetchedIssueDependencies with `now` after issuing
		// their own live fetch for a row not in the candidate map, which is
		// exactly the non-candidate population selectDependencyRefreshBatch
		// targets (and deliberately prioritises when a blockers_resolved
		// consumer is queued). Equal timestamps there mean "audited during
		// this tick, after we launched" — never "audited by the row's own
		// selection", because Gap E already guarantees a SELECTED row's
		// LastAuditedAt cannot equal the launch-time `now` (selection skips
		// rows audited at `now`).
		entry := state.DependencyAudit[refreshed.RequestKey]
		if entry == nil || !entry.LastAuditedAt.Before(result.StartedAt) {
			continue
		}
		// Final-review IMPORTANT-1: also check freshness on the DESTINATION
		// row, keyed by the fetched issue's CURRENT identity, before this
		// runs. On a key migration (e.g. an identifier-only key resolving to
		// an ID-keyed key once the tracker ID becomes known)
		// refreshed.RequestKey and dependencyAuditKey(refreshed.Issue) name
		// two different map entries. Checking only the request-key row's
		// freshness let a batch launched BEFORE a migration land its
		// (now-stale) snapshot straight onto the freshly-migrated
		// destination row without ever consulting THAT row's own
		// LastAuditedAt — the destination can have been created and had its
		// blockers_resolved transition fired and disarmed by the same-tick
		// candidate loop moments after this batch launched. Applying the
		// stale snapshot on top re-arms WasBlocked on the destination row, so
		// the next genuine Unblocked observation fires blockers_resolved a
		// second time. This is the same AUTO-1 signature as Gap C / N-1
		// above, reached via the migration path instead of the same-key
		// staleness path those two guard.
		newKey := dependencyAuditKey(refreshed.Issue)
		if newEntry := state.DependencyAudit[newKey]; newKey != "" && newEntry != nil && !newEntry.LastAuditedAt.Before(result.StartedAt) {
			continue
		}
		o.auditFetchedIssueDependenciesAndDispatch(ctx, state, refreshed.Issue, now)
		if newKey != "" && newKey != refreshed.RequestKey {
			// The issue's identity changed between audits; the audit above
			// wrote a row under the new key, so retire the old one.
			//
			// Losing-a-latch direction (pre-existing, deliberately left
			// unchanged by this edit): when newKey has no prior row,
			// auditIssueDependencies sees prev == nil, so WasBlocked /
			// FirstBlockedAt / LastTransitionVersion reset to zero on the new
			// row rather than carrying over the old key's history. That loss
			// was accepted before this fix and stays out of scope here — this
			// edit only closes the opposite (gaining) direction, where a
			// stale snapshot re-arms a latch a fresher audit had already
			// disarmed.
			delete(state.DependencyAudit, refreshed.RequestKey)
		}
		if entry := state.DependencyAudit[newKey]; entry != nil {
			entry.ConsecutiveFailures = 0
			entry.InFlight = false
			entry.LastRefreshAttemptAt = now
		}
	}

	if result.BlockersResolvedRan && result.BlockersResolvedOK {
		for i := range result.BlockersResolvedIssues {
			issue := result.BlockersResolvedIssues[i]
			// Gap C / N-1: same staleness guard as the result.Issues loop
			// above (see its comment for why !Before, not After), keyed on
			// dependencyAuditKey since this loop has no RequestKey. Unlike
			// that loop, a nil entry here is NOT a reason to skip: the
			// states-based fetch can surface an issue with no DependencyAudit
			// row yet (never audited before), and auditFetchedIssueDependencies
			// creating that row is exactly the intended behavior — only an
			// EXISTING row with a LastAuditedAt at-or-after this batch's
			// launch represents a superseded (or redundant) snapshot.
			if entry := state.DependencyAudit[dependencyAuditKey(issue)]; entry != nil && !entry.LastAuditedAt.Before(result.StartedAt) {
				continue
			}
			o.auditFetchedIssueDependenciesAndDispatch(ctx, state, issue, now)
		}
		// Gap A: advance the watermark to the seq observed at LAUNCH, not the
		// live seq. The fetch window stays open for the whole batch, and
		// poll-driven audits can bump DependencyTransitionSeq in the
		// meantime for issues this batch never fetched; absorbing those
		// bumps into the watermark would silently skip the rescan they are
		// supposed to trigger. A transition fired by THIS call's own
		// processing above (the loop over BlockersResolvedIssues) is, by the
		// same reasoning, also left unabsorbed — DependencyTransitionSeq will
		// sit ahead of the watermark afterward, which simply means the next
		// reconcile's seq comparison in pendingBlockersResolvedStates sees
		// "changed" and issues one more (safe, idempotent) fetch rather than
		// risking a missed one.
		state.LastBlockersResolvedAuditSeq = result.SeqAtLaunch
	}
}

// dependencyRefreshWatchdogGrace is added to the configured timeout before the
// event loop declares an in-flight batch lost. Covers the goroutine's own
// scheduling latency plus the send timeout.
const dependencyRefreshWatchdogGrace = 5 * time.Second

// dependencyRefreshTimeout / dependencyRefreshInterval / dependencyRefreshBatchSize
// read the three off-loop-refresh cfg fields and clamp a non-positive value to
// the shared config.Default* constant (Gap F). positiveIntField already
// floors these at load time for anything reachable from WORKFLOW.md, so a
// non-positive value here can only come from a hand-constructed config.Config
// (tests) — but if it ever reached production unclamped, batchSize<=0 would
// silently disable the refresh entirely, and context.WithTimeout(ctx,
// <=0-derived-duration) would make every batch launch, fetch nothing, skip
// the blockers-resolved scan, and still stamp LastRefreshAttemptAt on every
// selected row — arming the throttle against rows that were never actually
// refreshed. All three cfg fields are startup-only (no HTTP setter exists),
// so no cfgMu is needed to read them — same reasoning as StallTimeoutMs. See
// CLAUDE.md.
func dependencyRefreshTimeout(cfg *config.Config) time.Duration {
	if cfg.Agent.DependencyAuditRefreshTimeoutMs <= 0 {
		return time.Duration(config.DefaultDependencyAuditRefreshTimeoutMs) * time.Millisecond
	}
	return time.Duration(cfg.Agent.DependencyAuditRefreshTimeoutMs) * time.Millisecond
}

func dependencyRefreshInterval(cfg *config.Config) time.Duration {
	if cfg.Agent.DependencyAuditRefreshIntervalMs <= 0 {
		return time.Duration(config.DefaultDependencyAuditRefreshIntervalMs) * time.Millisecond
	}
	return time.Duration(cfg.Agent.DependencyAuditRefreshIntervalMs) * time.Millisecond
}

func dependencyRefreshBatchSize(cfg *config.Config) int {
	if cfg.Agent.DependencyAuditRefreshBatchSize <= 0 {
		return config.DefaultDependencyAuditRefreshBatchSize
	}
	return cfg.Agent.DependencyAuditRefreshBatchSize
}

// reclaimStuckDependencyRefresh is the dependency-refresh watchdog: it force-
// releases an in-flight batch that has run longer than its configured timeout
// plus grace. Split out from reconcileDependencyRefresh (Task 6 review Gap D)
// so onTick can call it BEFORE the candidate fetch — reconcileDependencyRefresh
// (batch selection) is only reached when FetchCandidateIssues succeeds, but a
// hung or permanently-broken tracker (revoked token, bad config) is exactly
// the condition this watchdog exists to recover from, and onTick returns
// early on a fetch error without ever reaching reconcileDependencyRefresh.
// Idempotent — safe to call again later in the same tick from
// reconcileDependencyRefresh itself (it will simply see DepsRefreshInFlight
// already false and no-op) so direct callers/tests of
// reconcileDependencyRefresh keep working unchanged.
func (o *Orchestrator) reclaimStuckDependencyRefresh(state *State, now time.Time) {
	if !state.DepsRefreshInFlight {
		return
	}
	timeout := dependencyRefreshTimeout(o.cfg)
	if now.Sub(state.DepsRefreshStartedAt) <= timeout+dependencyRefreshWatchdogGrace {
		return
	}
	slog.Warn("orchestrator: dependency refresh watchdog fired, releasing rows",
		"started_at", state.DepsRefreshStartedAt,
		"batch", state.DepsRefreshBatchSize,
		"generation", state.DepsRefreshGeneration)
	// Gap A/B: sweep ALL rows, not just the abandoned batch's own keys. A
	// mid-flight key migration (a candidate audit rekeying an
	// identifier-only row to an ID-keyed row while a batch holds the old
	// key) can orphan a row under a key the abandoned batch never held;
	// only an all-rows sweep reclaims it. Narrowing this to the batch's
	// keys would wedge those rows forever.
	for _, entry := range state.DependencyAudit {
		if entry != nil {
			entry.InFlight = false
		}
	}
	state.DepsRefreshInFlight = false
	state.DepsRefreshBatchSize = 0
	state.DepsRefreshStartedAt = time.Time{}
	// Gap B: bump the generation so the abandoned batch's result, if it
	// ever arrives, is recognised as stale and cannot clear the latch of
	// whatever batch is running by then.
	state.DepsRefreshGeneration++
}

// reconcileDependencyRefresh is the per-tick entry point. It runs the watchdog,
// selects a batch, and hands it to a background goroutine. It performs NO
// tracker I/O itself — that is the whole point of this function's existence.
func (o *Orchestrator) reconcileDependencyRefresh(ctx context.Context, state *State, now time.Time) {
	if o.tracker == nil {
		return
	}
	o.reclaimStuckDependencyRefresh(state, now)
	if state.DepsRefreshInFlight {
		return
	}

	timeout := dependencyRefreshTimeout(o.cfg)
	interval := dependencyRefreshInterval(o.cfg)
	batchSize := dependencyRefreshBatchSize(o.cfg)

	targets := selectDependencyRefreshBatch(state, now, interval, batchSize)
	states := o.pendingBlockersResolvedStates(state)
	if len(targets) == 0 && len(states) == 0 {
		return
	}

	state.DepsRefreshInFlight = true
	state.DepsRefreshStartedAt = now
	state.DepsRefreshBatchSize = len(targets)
	state.DepsRefreshGeneration++
	o.depsRefreshWg.Add(1)
	// Gap A: seqAtLaunch is captured HERE, before the fetch window opens, so
	// transitions recorded by other paths while the batch is in flight are not
	// absorbed by the watermark.
	go o.runDependencyRefresh(ctx, dependencyRefreshOrder{
		Targets:     targets,
		States:      states,
		Timeout:     timeout,
		StartedAt:   now,
		Generation:  state.DepsRefreshGeneration,
		SeqAtLaunch: state.DependencyTransitionSeq,
	})
}

// pendingBlockersResolvedStates returns the tracker states the blockers_resolved
// pass must scan, or nil when the pass would find nothing.
//
// The seq check is the same short-circuit the former inline implementation used:
// DependencyTransitionSeq advances on every blockers_resolved transition — the
// only condition under which the scan can find new work — so a stable seq
// guarantees the fetch would observe no actionable rows.
func (o *Orchestrator) pendingBlockersResolvedStates(state *State) []string {
	automations := o.snapBlockersResolvedAutomations()
	if len(automations) == 0 {
		return nil
	}
	if state.DependencyTransitionSeq == state.LastBlockersResolvedAuditSeq {
		return nil
	}
	var states []string
	for _, automation := range automations {
		states = append(states, automation.States...)
	}
	return deduplicateStringsFold(states)
}
