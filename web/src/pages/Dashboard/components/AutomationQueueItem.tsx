import type { AutomationQueueRow, DependencyAuditRow } from '../../../types/schemas';
import { blockerLabel, queuedAge, reasonLabel, statusTone } from './automationQueueModel';

export function AutomationQueueItem({
  row,
  dependency,
  onSelectIssue,
  onSelectQueue,
}: {
  row: AutomationQueueRow;
  dependency?: DependencyAuditRow;
  onSelectIssue: (identifier: string) => void;
  onSelectQueue: (queueId: string) => void;
}) {
  const unresolved = dependency?.unresolvedBlockers ?? [];
  return (
    <div
      data-testid={`automation-queue-card-${row.identifier}`}
      className="grid gap-2 px-4 py-3 lg:grid-cols-[1.1fr_0.8fr_1.1fr_1fr_0.9fr_1fr_0.7fr_0.6fr_0.7fr] lg:items-center lg:gap-3"
    >
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          <span className="text-theme-accent min-w-0 truncate font-mono text-xs font-semibold">
            {row.automationId}
          </span>
          <span
            className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${statusTone(row.status)}`}
          >
            {row.status}
          </span>
        </div>
      </div>
      <div className="text-theme-text-secondary font-mono text-[11px]">{row.triggerType}</div>
      <div className="min-w-0">
        <button
          type="button"
          aria-label={`Open issue ${row.identifier}`}
          onClick={() => {
            onSelectIssue(row.identifier);
          }}
          className="text-theme-text hover:text-theme-accent min-w-0 truncate font-mono text-xs font-semibold"
        >
          {row.identifier}
        </button>
        {row.title && (
          <div className="text-theme-muted mt-0.5 truncate text-[11px]" title={row.title}>
            {row.title}
          </div>
        )}
      </div>
      <div className="flex min-w-0 flex-wrap gap-1">
        {unresolved.length === 0 ? (
          <span className="text-theme-muted text-[11px]">none</span>
        ) : (
          unresolved.map((blocker) => (
            <span
              key={blockerLabel(blocker)}
              className="bg-theme-warning-soft text-theme-warning rounded px-1.5 py-0.5 font-mono text-[10px]"
            >
              {blockerLabel(blocker)}
            </span>
          ))
        )}
      </div>
      <div className="text-theme-text-secondary flex min-w-0 flex-wrap gap-1 text-xs">
        <span className="truncate">{row.profile}</span>
        {row.backend ? <span className="text-theme-muted truncate">{row.backend}</span> : null}
      </div>
      <div className="min-w-0">
        <span className="text-theme-text-secondary text-xs">{reasonLabel(row)}</span>
        {row.reasonDetail && (
          <div className="text-theme-muted mt-0.5 truncate text-[11px]" title={row.reasonDetail}>
            {row.reasonDetail}
          </div>
        )}
      </div>
      <div className="text-theme-muted font-mono text-[11px]" title={row.queuedAt}>
        {queuedAge(row.queuedAt)}
      </div>
      <div className="text-theme-text-secondary text-xs">
        {row.attemptCount === 1 ? '1 attempt' : `${String(row.attemptCount)} attempts`}
      </div>
      <div>
        <button
          type="button"
          aria-label={`Open ${row.identifier} details`}
          onClick={() => {
            onSelectQueue(row.id);
          }}
          className="border-theme-line text-theme-accent min-h-9 rounded-[var(--radius-sm)] border px-2.5 py-1 text-[11px] font-medium hover:opacity-80"
        >
          Details
        </button>
      </div>
    </div>
  );
}
