import type { RunningRow } from '../../../types/schemas';
import { queuedAge } from './automationQueueModel';

// Visual sibling of AutomationQueueItem for the
// "Running" section of AutomationQueueList. Mirrors the queue item's grid
// columns, font sizes, and palette so the two sections feel like one panel.
// RunningRow does not carry title or profile (those live on the issue
// snapshot map) — the operator gets to both by clicking through to the
// issue detail, just like the queue row does.
export function AutomationRunningItem({
  row,
  onSelectIssue,
}: {
  row: RunningRow;
  onSelectIssue: (identifier: string) => void;
}) {
  return (
    <div
      data-testid={`automation-running-card-${row.identifier}`}
      className="grid gap-2 px-4 py-3 lg:grid-cols-[1.1fr_0.8fr_1.1fr_0.9fr_0.7fr] lg:items-center lg:gap-3"
    >
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          <span className="text-theme-accent min-w-0 truncate font-mono text-xs font-semibold">
            {row.automationId ?? '—'}
          </span>
          <span className="bg-theme-success-soft text-theme-success rounded px-1.5 py-0.5 text-[10px] font-medium">
            running
          </span>
        </div>
      </div>
      <div className="text-theme-text-secondary font-mono text-[11px]">
        {row.triggerType ?? '—'}
      </div>
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
        <div className="text-theme-muted mt-0.5 truncate text-[11px]" title={row.state}>
          {row.state}
        </div>
      </div>
      <div className="text-theme-text-secondary text-xs">{row.backend ?? '—'}</div>
      <div className="text-theme-muted font-mono text-[11px]" title={row.startedAt}>
        {queuedAge(row.startedAt)}
      </div>
    </div>
  );
}
