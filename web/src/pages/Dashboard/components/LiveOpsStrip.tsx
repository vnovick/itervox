import { useEffect, useMemo, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../../store/itervoxStore';
import {
  LiveIndicator,
  type Status as LiveIndicatorStatus,
} from '../../../components/ui/LiveIndicator/LiveIndicator';
import type { StateSnapshot } from '../../../types/schemas';
import { automationsFiredToday } from './dashboardMetrics';
import { OpsChip } from './OpsChip';
import { DispatchPressureChip } from './DispatchPressureChip';
import { BackendHealthChips } from './BackendHealthChips';
import { useDashboardHref } from '../../../hooks/useDashboardHref';
import {
  ACTIVITY_LABEL,
  STATUS_META,
  summarizeStatus,
  type StatusActivity,
} from '../../../lib/statusModel';

// CORE-076 — 'active' (agents running) replaces the old 'live', which also
// meant "SSE connected" in the header. Live is the connection only.
type LiveOpsStatus = StatusActivity;

export interface LiveOpsStripModel {
  status: LiveOpsStatus;
  capacityLabel: string;
  runningCount: number;
  queueCount: number;
  queueLabel: string;
  queueSaturated: boolean;
  blockedQueueCount: number;
  // dependencyBlockedCount is "unresolved" — blocked + unknown — matching
  // dispatch's behaviour. v0.2.0 audit P1-8.
  dependencyBlockedCount: number;
  // dependencyUnknownCount is the parenthetical breakdown shown to operators
  // who need to know whether tracker data is incomplete vs. genuinely blocked.
  dependencyUnknownCount: number;
  recentlyUnblockedCount: number;
  retryCount: number;
  pausedCount: number;
  // CORE-076 — input_required rows only (summarizeStatus.needsInput);
  // pending resumes are resumingCount.
  inputRequiredCount: number;
  resumingCount: number;
  sshLabel: string | null;
  automationsToday: number;
  // selfReentryDrops is the monotonic count of input_required automation
  // dispatches suppressed by the self-reentry guard — lets operators tell
  // "guarded loop" apart from "automation never fired". gaps_11 G-11.
  selfReentryDrops: number;
  // depsRefreshingCount is how many dependency-audit rows the daemon is
  // currently re-fetching off the event loop. Non-zero means "working", not
  // "stuck" — the distinction operators could not previously make.
  depsRefreshingCount: number;
  // depsDegradedCount is rows whose refresh has failed repeatedly. They stay
  // blocked; the data behind that decision is stale.
  depsDegradedCount: number;
  // cycleCount is this tick's strongly-connected-component dependency cycle
  // count (critical-path-ordering Task 4/6) — issues that can never become
  // dispatchable through normal blocker resolution without operator
  // intervention.
  cycleCount: number;
  // attentionCount is the derived operator-attention entry count
  // (critical-path-ordering Task 5/6) — cycle members plus issues blocked
  // past `dependencies.escalate_blocked_after_hours`.
  attentionCount: number;
  // outboxPendingCount / outboxDegradedCount — write-ahead-outbox design,
  // Task 4. Pending is the total entry count (durable writes not yet
  // flushed to the tracker); degraded is the subset that crossed the
  // operator-visible-error-badge threshold and needs a look at the Outbox
  // panel (Retry or Discard).
  outboxPendingCount: number;
  outboxDegradedCount: number;
  // trackerRateLimitedUntil is the latest `rateLimitedUntil` (ISO string)
  // across outboxEntries that is still in the future, or null when nothing
  // is currently rate limited. Task 9 — this reads the outbox's view of the
  // rate limit, not tracker.SharedRateLimitGate() (process state with no
  // snapshot field); an open gate with an empty outbox therefore shows
  // nothing, which is acceptable since there is no pending write to act on.
  trackerRateLimitedUntil: string | null;
  // CORE-084 — the tracker API request budget from snapshot.rateLimits
  // (populated only when the tracker implements tracker.RateLimiter). null
  // hides the chip; `percent: null` means the daemon reported no usable limit
  // (requestsLimit <= 0) and the chip says "unknown" instead of dividing.
  trackerBudget: TrackerBudget | null;
}

export interface TrackerBudget {
  percent: number | null;
  remaining: number;
  limit: number;
  resetAt: string | null;
  low: boolean;
}

// Below this share of the request budget the chip turns warning-toned.
const TRACKER_BUDGET_WARN_PCT = 10;

function trackerBudget(rateLimits: StateSnapshot['rateLimits'] | undefined): TrackerBudget | null {
  if (!rateLimits) return null;
  const { requestsLimit: limit, requestsRemaining: remaining } = rateLimits;
  const resetAt = rateLimits.requestsReset ?? null;
  if (limit <= 0) return { percent: null, remaining, limit, resetAt, low: false };
  const percent = Math.max(0, Math.min(100, Math.floor((remaining / limit) * 100)));
  return { percent, remaining, limit, resetAt, low: percent < TRACKER_BUDGET_WARN_PCT };
}

// The snapshot branches liveOpsStripModel reads. LiveOpsStrip selects exactly
// these (CORE-074), so a push that only changes other fields — including the
// per-build generatedAt stamp — does not re-render it.
export type LiveOpsStripInput = Pick<
  StateSnapshot,
  | 'running'
  | 'maxConcurrentAgents'
  | 'automationQueue'
  | 'automationQueueBackpressure'
  | 'sshHosts'
  | 'dependencyAudit'
  | 'retrying'
  | 'paused'
  | 'inputRequired'
  | 'history'
  | 'automationDropsSelfReentryTotal'
  | 'depsRefreshInFlight'
  | 'depsRefreshDegradedCount'
  | 'dependencyCycles'
  | 'dependencyAttention'
  | 'outboxEntries'
  | 'rateLimits'
>;

// The model's clock-dependent parts (automations "today", the tracker
// rate-limit chip's expiry) advance on this ticker, not on snapshot pushes.
export const LIVE_OPS_CLOCK_TICK_MS = 30_000;

function useCoarseNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => {
      setNow(Date.now());
    }, intervalMs);
    return () => {
      clearInterval(id);
    };
  }, [intervalMs]);
  return now;
}

export function liveOpsStripModel(
  snapshot: LiveOpsStripInput | null,
  now = Date.now(),
): LiveOpsStripModel {
  if (!snapshot) {
    return {
      status: 'offline',
      capacityLabel: '0/0',
      runningCount: 0,
      queueCount: 0,
      queueLabel: '0',
      queueSaturated: false,
      blockedQueueCount: 0,
      dependencyBlockedCount: 0,
      dependencyUnknownCount: 0,
      recentlyUnblockedCount: 0,
      retryCount: 0,
      pausedCount: 0,
      inputRequiredCount: 0,
      resumingCount: 0,
      sshLabel: null,
      automationsToday: 0,
      selfReentryDrops: 0,
      depsRefreshingCount: 0,
      depsDegradedCount: 0,
      cycleCount: 0,
      attentionCount: 0,
      outboxPendingCount: 0,
      outboxDegradedCount: 0,
      trackerRateLimitedUntil: null,
      trackerBudget: null,
    };
  }

  const summary = summarizeStatus(snapshot, { sseConnected: false });
  const runningCount = summary.running;
  const maxConcurrent = snapshot.maxConcurrentAgents;
  const queue = snapshot.automationQueue ?? [];
  const backpressure = snapshot.automationQueueBackpressure;
  const queueCount = backpressure?.length ?? queue.length;
  const queueLabel = backpressure
    ? `${String(backpressure.length)}/${String(backpressure.maxLength)}`
    : String(queue.length);
  const sshHosts = new Set(snapshot.sshHosts?.map((host) => host.host) ?? []);
  const activeSSH = snapshot.running.filter((row) => row.workerHost).length;
  for (const row of snapshot.running) {
    if (row.workerHost) sshHosts.add(row.workerHost);
  }

  return {
    status: summary.activity,
    runningCount,
    capacityLabel:
      maxConcurrent > 0 ? `${String(runningCount)}/${String(maxConcurrent)}` : String(runningCount),
    queueCount,
    queueLabel,
    queueSaturated: Boolean(backpressure?.saturated || backpressure?.pausedProducers),
    blockedQueueCount: queue.filter((row) => row.status === 'blocked').length,
    // v0.2.0 audit P1-8 — dispatch treats `unknown` blocker status the same
    // as `blocked` (the orchestrator refuses to dispatch issues whose
    // blockers cannot be proven terminal). The dashboard chip must reflect
    // that semantic; counting only `status === 'blocked'` understated the
    // operationally-relevant total. Surface the unknown breakdown
    // separately so operators can see whether tracker data is incomplete.
    dependencyBlockedCount: (snapshot.dependencyAudit ?? []).filter(
      (row) => row.status === 'blocked' || row.status === 'unknown',
    ).length,
    dependencyUnknownCount: (snapshot.dependencyAudit ?? []).filter(
      (row) => row.status === 'unknown',
    ).length,
    recentlyUnblockedCount: (snapshot.dependencyAudit ?? []).filter(
      (row) => row.status === 'unblocked' && row.unblockedAt,
    ).length,
    retryCount: summary.retrying,
    pausedCount: summary.paused,
    inputRequiredCount: summary.needsInput,
    resumingCount: summary.resuming,
    sshLabel:
      sshHosts.size > 0 || activeSSH > 0
        ? `${String(sshHosts.size)} host${sshHosts.size === 1 ? '' : 's'} · ${String(activeSSH)} active`
        : null,
    automationsToday: automationsFiredToday(snapshot.history ?? [], now),
    selfReentryDrops: snapshot.automationDropsSelfReentryTotal ?? 0,
    depsRefreshingCount: snapshot.depsRefreshInFlight ?? 0,
    depsDegradedCount: snapshot.depsRefreshDegradedCount ?? 0,
    cycleCount: (snapshot.dependencyCycles ?? []).length,
    attentionCount: (snapshot.dependencyAttention ?? []).length,
    outboxPendingCount: (snapshot.outboxEntries ?? []).length,
    outboxDegradedCount: (snapshot.outboxEntries ?? []).filter((row) => row.degraded).length,
    trackerRateLimitedUntil: latestFutureRateLimitedUntil(snapshot.outboxEntries ?? [], now),
    trackerBudget: trackerBudget(snapshot.rateLimits),
  };
}

// latestFutureRateLimitedUntil picks the latest outboxEntries[].rateLimitedUntil
// that is strictly after `now`, returning it as the original ISO string (or
// null when none qualify). Kept pure — no Date.now() — so the derivation is
// stable given the same (snapshot, now) inputs. Task 9.
function latestFutureRateLimitedUntil(
  entries: readonly { rateLimitedUntil?: string }[],
  now: number,
): string | null {
  let latest: { iso: string; ms: number } | null = null;
  for (const entry of entries) {
    if (!entry.rateLimitedUntil) continue;
    const ms = new Date(entry.rateLimitedUntil).getTime();
    if (!Number.isFinite(ms) || ms <= now) continue;
    if (!latest || ms > latest.ms) latest = { iso: entry.rateLimitedUntil, ms };
  }
  return latest?.iso ?? null;
}

// depsChipLabel renders the deps chip's base "N blocked"/"unresolved" text
// plus a "refreshing N" / "N stale" suffix so the off-loop refresher's
// activity (Task 6) is visible to the operator instead of silent (Task 7).
//
// A states-only refresh batch (blockers-resolved scan with no row targets)
// legitimately has depsRefreshingCount === 0 while the daemon is still
// mid-batch; the `> 0` guard below deliberately renders no "refreshing"
// suffix in that case rather than a confusing "refreshing 0".
function depsChipLabel(model: LiveOpsStripModel): string {
  const base =
    model.dependencyUnknownCount > 0
      ? `Deps ${String(model.dependencyBlockedCount)} unresolved (${String(model.dependencyUnknownCount)} unknown)`
      : `Deps ${String(model.dependencyBlockedCount)} blocked`;
  const suffixes: string[] = [];
  if (model.depsRefreshingCount > 0)
    suffixes.push(`refreshing ${String(model.depsRefreshingCount)}`);
  if (model.depsDegradedCount > 0) suffixes.push(`${String(model.depsDegradedCount)} stale`);
  return suffixes.length > 0 ? `${base} · ${suffixes.join(' · ')}` : base;
}

// dependencyAttentionChipLabel renders the cycle/attention tile's text.
// critical-path-ordering Task 4/5/6 — cycles (strongly-connected-component
// dependency loops) and operator-attention entries (cycle members plus
// stale-blocked issues) were previously invisible to operators; this tile
// surfaces both counts. Hidden entirely at zero — see the render-site guard
// in LiveOpsStrip below, mirroring the self-reentry-drops chip's
// only-when-nonzero convention.
function dependencyAttentionChipLabel(model: LiveOpsStripModel): string {
  const parts: string[] = [];
  if (model.cycleCount > 0) {
    parts.push(`${String(model.cycleCount)} dependency cycle${model.cycleCount === 1 ? '' : 's'}`);
  }
  if (model.attentionCount > 0) {
    parts.push(`${String(model.attentionCount)} need attention`);
  }
  return parts.join(' · ');
}

export function LiveOpsStrip() {
  // CORE-074 — branch selector, not the whole snapshot: with the store's
  // structural sharing, unchanged branches keep their identity, so a
  // timestamp-only push leaves every selected value equal and skips the
  // render. The whole-snapshot selector re-rendered on every push.
  const branches = useItervoxStore(
    useShallow((state) => {
      const s = state.snapshot;
      return {
        hasSnapshot: s !== null,
        running: s?.running,
        maxConcurrentAgents: s?.maxConcurrentAgents,
        automationQueue: s?.automationQueue,
        automationQueueBackpressure: s?.automationQueueBackpressure,
        sshHosts: s?.sshHosts,
        dependencyAudit: s?.dependencyAudit,
        retrying: s?.retrying,
        paused: s?.paused,
        inputRequired: s?.inputRequired,
        history: s?.history,
        automationDropsSelfReentryTotal: s?.automationDropsSelfReentryTotal,
        depsRefreshInFlight: s?.depsRefreshInFlight,
        depsRefreshDegradedCount: s?.depsRefreshDegradedCount,
        dependencyCycles: s?.dependencyCycles,
        dependencyAttention: s?.dependencyAttention,
        outboxEntries: s?.outboxEntries,
        rateLimits: s?.rateLimits,
        dispatchPressure: s?.dispatchPressure,
        backendHealth: s?.backendHealth,
        autoSwitches: s?.autoSwitches,
      };
    }),
  );
  const now = useCoarseNow(LIVE_OPS_CLOCK_TICK_MS);
  const dashboardHref = useDashboardHref();
  // CORE-086 — a count links only while there is something behind it.
  const linkWhen = (count: number, target: Parameters<typeof dashboardHref>[0]) =>
    count > 0 ? dashboardHref(target) : undefined;
  const {
    hasSnapshot,
    running,
    maxConcurrentAgents,
    automationQueue,
    automationQueueBackpressure,
    sshHosts,
    dependencyAudit,
    retrying,
    paused,
    inputRequired,
    history,
    automationDropsSelfReentryTotal,
    depsRefreshInFlight,
    depsRefreshDegradedCount,
    dependencyCycles,
    dependencyAttention,
    outboxEntries,
    rateLimits,
  } = branches;
  const model = useMemo(
    () =>
      liveOpsStripModel(
        hasSnapshot && running && retrying && paused && maxConcurrentAgents !== undefined
          ? {
              running,
              maxConcurrentAgents,
              automationQueue,
              automationQueueBackpressure,
              sshHosts,
              dependencyAudit,
              retrying,
              paused,
              inputRequired,
              history,
              automationDropsSelfReentryTotal,
              depsRefreshInFlight,
              depsRefreshDegradedCount,
              dependencyCycles,
              dependencyAttention,
              outboxEntries,
              rateLimits: rateLimits ?? null,
            }
          : null,
        now,
      ),
    [
      hasSnapshot,
      running,
      maxConcurrentAgents,
      automationQueue,
      automationQueueBackpressure,
      sshHosts,
      dependencyAudit,
      retrying,
      paused,
      inputRequired,
      history,
      automationDropsSelfReentryTotal,
      depsRefreshInFlight,
      depsRefreshDegradedCount,
      dependencyCycles,
      dependencyAttention,
      outboxEntries,
      rateLimits,
      now,
    ],
  );
  const statusLabel = ACTIVITY_LABEL[model.status];
  const indicatorStatus: LiveIndicatorStatus =
    model.status === 'active' ? 'live' : model.status === 'waiting' ? 'idle' : 'error';

  return (
    <section
      className={`border-theme-line bg-theme-bg-elevated rounded-[var(--radius-md)] border px-3 py-2.5 ${
        model.queueSaturated ? 'border-theme-danger bg-theme-danger-soft' : ''
      }`}
      data-testid="live-ops-strip"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2" data-testid="live-ops-chip-row">
        {model.queueSaturated && (
          <span
            role="status"
            aria-label="Automation queue full"
            className="bg-theme-danger inline-flex min-h-9 max-w-full items-center rounded-[var(--radius-sm)] px-2.5 py-1 text-[12px] leading-snug font-semibold whitespace-normal text-white"
          >
            Automation queue full {model.queueLabel} · new automation triggers paused
          </span>
        )}
        <span className="text-theme-text inline-flex min-h-9 items-center rounded-[var(--radius-sm)] px-2 text-[12px] font-semibold">
          <LiveIndicator status={indicatorStatus} size="sm" label={statusLabel} />
        </span>
        <OpsChip
          label={`Capacity ${model.capacityLabel}`}
          title={`${String(model.runningCount)} ${STATUS_META.running.noun}`}
          statusKey="running"
          statusCount={model.runningCount}
        />
        {/* Capacity above is the instantaneous gauge (running/max). This tile
            is the cumulative counterpart: whether that capacity has actually
            been the binding constraint, which is what tells the operator if
            raising max_concurrent_agents would buy anything. Hidden until the
            daemon completes a tick. */}
        <DispatchPressureChip pressure={branches.dispatchPressure} />
        <OpsChip label={`Queue ${model.queueLabel}`} danger={model.queueSaturated} />
        <OpsChip label={`Blocked ${String(model.blockedQueueCount)}`} />
        <OpsChip
          label={depsChipLabel(model)}
          danger={model.depsDegradedCount > 0}
          to={linkWhen(model.dependencyBlockedCount, 'deps-view')}
        />
        <OpsChip label={`Unblocked ${String(model.recentlyUnblockedCount)}`} />
        <OpsChip
          label={`${STATUS_META.input_required.label} ${String(model.inputRequiredCount)}`}
          statusKey="input_required"
          statusCount={model.inputRequiredCount}
          warning={model.inputRequiredCount > 0}
          to={linkWhen(model.inputRequiredCount, 'attention-inbox')}
        />
        {model.resumingCount > 0 && (
          <OpsChip
            label={`${STATUS_META.pending_input_resume.label} ${String(model.resumingCount)}`}
            statusKey="pending_input_resume"
            statusCount={model.resumingCount}
            to={linkWhen(model.resumingCount, 'pending-resume')}
          />
        )}
        <OpsChip
          label={`${STATUS_META.retrying.label} ${String(model.retryCount)}`}
          statusKey="retrying"
          statusCount={model.retryCount}
          to={linkWhen(model.retryCount, 'retry-queue')}
        />
        <OpsChip
          label={`${STATUS_META.paused.label} ${String(model.pausedCount)}`}
          statusKey="paused"
          statusCount={model.pausedCount}
          to={linkWhen(model.pausedCount, 'running-sessions')}
        />
        {model.sshLabel && <OpsChip label={`SSH ${model.sshLabel}`} />}
        <OpsChip label={`Automations ${String(model.automationsToday)} today`} />
        {/* gaps_11 G-11 — only rendered once the guard has actually fired so
            the strip stays compact on healthy daemons. */}
        {model.selfReentryDrops > 0 && (
          <OpsChip label={`Self-reentry drops ${String(model.selfReentryDrops)}`} />
        )}
        {/* critical-path-ordering Task 4/5/6 — cycles/attention tile, hidden
            when both counts are zero (same "compact when healthy" convention
            as the self-reentry-drops chip above). Cycles are the more severe
            condition (danger), a stale-blocker-only attention count is
            warning-severity. */}
        {(model.cycleCount > 0 || model.attentionCount > 0) && (
          <OpsChip
            label={dependencyAttentionChipLabel(model)}
            danger={model.cycleCount > 0}
            warning={model.cycleCount === 0 && model.attentionCount > 0}
          />
        )}
        {/* outbox Task 4 — pending/degraded write-ahead-outbox tile, hidden
            at zero (same "compact when healthy" convention as the two
            chips above). Danger once anything is degraded; otherwise a
            plain (non-alarming — pending writes are normal, expected
            operation) info-tone chip. */}
        {model.outboxPendingCount > 0 && (
          <OpsChip
            label={outboxChipLabel(model)}
            danger={model.outboxDegradedCount > 0}
            to={dashboardHref('outbox')}
          />
        )}
        {/* Task 9 — fleet-level indicator, mirroring the per-row rate-limited
            chip in OutboxList. Hidden when nothing is currently rate
            limited (same "compact when healthy" convention as the other
            conditional tiles above). */}
        {model.trackerRateLimitedUntil && (
          <OpsChip
            warning
            label={`Tracker rate limited until ${formatHHMM(model.trackerRateLimitedUntil)}`}
            title={`Tracker rate limited until ${new Date(model.trackerRateLimitedUntil).toLocaleString()}`}
          />
        )}
        {/* CORE-084 — tracker API request budget (snapshot.rateLimits). A
            different signal from the chip above: that one says a write is
            already deferred; this one says how much budget is left. Both can
            show at once. Hidden when the tracker reports no budget at all. */}
        {model.trackerBudget && (
          <OpsChip
            testId="tracker-api-budget-chip"
            warning={model.trackerBudget.low}
            label={
              model.trackerBudget.percent === null
                ? 'Tracker API budget: unknown'
                : `Tracker API budget ${String(model.trackerBudget.percent)}%`
            }
            title={trackerBudgetTitle(model.trackerBudget)}
          />
        )}
        {/* CORE-055 — agent backend health, beside (never merged with) the
            tracker chip above; labelled by backend. */}
        <BackendHealthChips rows={branches.backendHealth} autoSwitches={branches.autoSwitches} />
      </div>
    </section>
  );
}

// formatHHMM matches the per-row rate-limited chip's time format in
// OutboxList.tsx (toLocaleTimeString with hour/minute only).
function formatHHMM(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function trackerBudgetTitle(budget: TrackerBudget): string {
  if (budget.percent === null) {
    return 'The tracker reported no request limit, so the remaining budget is unknown';
  }
  const reset = budget.resetAt ? ` · resets ${formatHHMM(budget.resetAt)}` : '';
  return `Tracker API: ${String(budget.remaining)} of ${String(budget.limit)} requests left${reset}`;
}

// outboxChipLabel renders the outbox tile's text: "Outbox N pending" plus a
// " · N degraded" suffix once any entry crosses the degraded threshold.
function outboxChipLabel(model: LiveOpsStripModel): string {
  const base = `Outbox ${String(model.outboxPendingCount)} pending`;
  return model.outboxDegradedCount > 0
    ? `${base} · ${String(model.outboxDegradedCount)} degraded`
    : base;
}

// OpsChip now lives in ./OpsChip so DispatchPressureChip can render the same
// tile without importing from this module (which would create a cycle, since
// this module imports that one).
export { OpsChip } from './OpsChip';
