import type { AutomationQueueRow, DependencyAuditRow, RunningRow } from '../../../types/schemas';

export type DependencyByIssue = Map<string, DependencyAuditRow>;

function queuePriority(row: AutomationQueueRow): number {
  if (row.status === 'blocked') return 0;
  if (row.triggerType === 'blockers_resolved' && row.reason === 'ready') return 1;
  if (row.status === 'dispatching') return 3;
  return 2;
}

export function sortAutomationQueueRows(rows: readonly AutomationQueueRow[]): AutomationQueueRow[] {
  return [...rows].sort((a, b) => {
    const pa = queuePriority(a);
    const pb = queuePriority(b);
    if (pa !== pb) return pa - pb;
    return Date.parse(a.queuedAt) - Date.parse(b.queuedAt);
  });
}

export function dependencyMap(rows: readonly DependencyAuditRow[]): DependencyByIssue {
  return new Map(rows.map((row) => [row.identifier, row]));
}

export function blockerLabel(blocker: { identifier?: string; id?: string; url?: string }): string {
  return blocker.identifier ?? blocker.id ?? blocker.url ?? 'unknown';
}

export function reasonLabel(row: AutomationQueueRow): string {
  switch (row.reason) {
    case 'no_slots':
      return 'no slots';
    case 'per_state_limit':
      return 'per-state limit';
    case 'dependency_blocked':
      return 'dependency blocked';
    case 'ready':
      return row.moveToState ? `move to ${row.moveToState}` : 'ready';
    default:
      return row.reason.replace(/_/g, ' ');
  }
}

function queueSearchText(
  row: AutomationQueueRow,
  dependency: DependencyAuditRow | undefined,
): string {
  const blockers = [
    ...(dependency?.blockedBy ?? []),
    ...(dependency?.unresolvedBlockers ?? []),
    ...(dependency?.resolvedBlockers ?? []),
  ]
    .map(blockerLabel)
    .join(' ');
  return [
    row.automationId,
    row.triggerType,
    row.identifier,
    row.title,
    row.issueState,
    row.profile,
    row.backend,
    row.status,
    row.reason,
    row.reasonDetail,
    row.cron,
    row.timezone,
    row.prUrl,
    row.inputContext,
    row.errorMessage,
    row.switchedToProfile,
    row.switchedToBackend,
    row.moveToState,
    blockers,
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
}

export function filterQueueRows(
  rows: readonly AutomationQueueRow[],
  dependencies: DependencyByIssue,
  query: string,
): AutomationQueueRow[] {
  const q = query.trim().toLowerCase();
  const sorted = sortAutomationQueueRows(rows);
  if (!q) return sorted;
  return sorted.filter((row) => queueSearchText(row, dependencies.get(row.identifier)).includes(q));
}

// Surfaces running automations in the queue panel
// (path 1 of dispatchOrQueueAutomation). Reuses queue's search + sort idioms.

function runningAutomationSearchText(row: RunningRow): string {
  // RunningRow doesn't carry title/profile — those live on the issue
  // snapshot map; the operator gets them by clicking through to the issue
  // detail. Search fields are the ones the row actually renders.
  return [row.identifier, row.state, row.backend, row.automationId, row.triggerType, row.workerHost]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
}

export function filterRunningAutomations(
  running: readonly RunningRow[] | undefined,
  query: string,
): RunningRow[] {
  const automations = (running ?? []).filter((r) => r.kind === 'automation');
  const sorted = [...automations].sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt));
  const q = query.trim().toLowerCase();
  if (!q) return sorted;
  return sorted.filter((row) => runningAutomationSearchText(row).includes(q));
}

export function queuedAge(queuedAt: string): string {
  const diff = Date.now() - Date.parse(queuedAt);
  if (Number.isNaN(diff)) return 'unknown';
  const mins = Math.max(0, Math.round(diff / 60_000));
  if (mins < 60) return `${String(mins)}m ago`;
  return `${String(Math.round(mins / 60))}h ago`;
}

export function statusTone(status: AutomationQueueRow['status']): string {
  switch (status) {
    case 'blocked':
      return 'bg-theme-warning-soft text-theme-warning';
    case 'dispatching':
      return 'bg-theme-accent-soft text-theme-accent-strong';
    default:
      return 'bg-theme-bg-soft text-theme-text-secondary';
  }
}
