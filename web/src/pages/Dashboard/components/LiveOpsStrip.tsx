import { useMemo } from 'react';
import { useItervoxStore } from '../../../store/itervoxStore';
import {
  LiveIndicator,
  type Status as LiveIndicatorStatus,
} from '../../../components/ui/LiveIndicator/LiveIndicator';
import type { StateSnapshot } from '../../../types/schemas';
import { automationsFiredToday } from './dashboardMetrics';

type LiveOpsStatus = 'live' | 'waiting' | 'offline';

export interface LiveOpsStripModel {
  status: LiveOpsStatus;
  capacityLabel: string;
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
  inputRequiredCount: number;
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
}

export function liveOpsStripModel(
  snapshot: StateSnapshot | null,
  now = Date.now(),
): LiveOpsStripModel {
  if (!snapshot) {
    return {
      status: 'offline',
      capacityLabel: '0/0',
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
      sshLabel: null,
      automationsToday: 0,
      selfReentryDrops: 0,
      depsRefreshingCount: 0,
      depsDegradedCount: 0,
      cycleCount: 0,
      attentionCount: 0,
      outboxPendingCount: 0,
      outboxDegradedCount: 0,
    };
  }

  const runningCount = snapshot.running.length;
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
    status: runningCount > 0 ? 'live' : 'waiting',
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
    retryCount: snapshot.retrying.length,
    pausedCount: snapshot.paused.length,
    inputRequiredCount: (snapshot.inputRequired ?? []).length,
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
  };
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
  const snapshot = useItervoxStore((state) => state.snapshot);
  const model = useMemo(() => liveOpsStripModel(snapshot), [snapshot]);
  const statusLabel =
    model.status === 'live' ? 'Live' : model.status === 'waiting' ? 'Waiting' : 'Offline';
  const indicatorStatus: LiveIndicatorStatus =
    model.status === 'live' ? 'live' : model.status === 'waiting' ? 'idle' : 'error';

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
        <OpsChip label={`Capacity ${model.capacityLabel}`} />
        <OpsChip label={`Queue ${model.queueLabel}`} danger={model.queueSaturated} />
        <OpsChip label={`Blocked ${String(model.blockedQueueCount)}`} />
        <OpsChip label={depsChipLabel(model)} danger={model.depsDegradedCount > 0} />
        <OpsChip label={`Unblocked ${String(model.recentlyUnblockedCount)}`} />
        <OpsChip label={`Input ${String(model.inputRequiredCount)}`} />
        <OpsChip label={`Retry ${String(model.retryCount)}`} />
        <OpsChip label={`Paused ${String(model.pausedCount)}`} />
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
          <OpsChip label={outboxChipLabel(model)} danger={model.outboxDegradedCount > 0} />
        )}
      </div>
    </section>
  );
}

// outboxChipLabel renders the outbox tile's text: "Outbox N pending" plus a
// " · N degraded" suffix once any entry crosses the degraded threshold.
function outboxChipLabel(model: LiveOpsStripModel): string {
  const base = `Outbox ${String(model.outboxPendingCount)} pending`;
  return model.outboxDegradedCount > 0
    ? `${base} · ${String(model.outboxDegradedCount)} degraded`
    : base;
}

function OpsChip({
  label,
  danger = false,
  warning = false,
}: {
  label: string;
  danger?: boolean;
  // critical-path-ordering Task 6 — second severity tone alongside `danger`,
  // for conditions that warrant operator attention but are less severe than
  // a hard blocker (e.g. stale-blocked issues vs. dependency cycles).
  warning?: boolean;
}) {
  return (
    <span
      className={`inline-flex min-h-9 flex-shrink-0 items-center rounded-[var(--radius-sm)] px-2.5 py-1 text-[12px] font-medium whitespace-nowrap ${
        danger
          ? 'bg-theme-danger-soft text-theme-danger'
          : warning
            ? 'bg-theme-warning-soft text-theme-warning'
            : 'bg-theme-bg-soft text-theme-text-secondary'
      }`}
    >
      {label}
    </span>
  );
}
