import type { AutomationQueueRow, DependencyAuditRow } from '../../../types/schemas';
import { plainButtonClass } from '../../../components/ui/button/buttonRecipe';
import { blockerLabel, queuedAge, reasonLabel, statusTone } from './automationQueueModel';

const DETAILS_BUTTON = plainButtonClass('secondary', 'sm', { pill: true, extra: 'min-h-9' });

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
    // Narrow panel (no column header): a compact card, the name on its own line
    // and the other fields wrapping inline beneath it. Wide panel: a table row.
    <div
      data-testid={`automation-queue-card-${row.identifier}`}
      className="flex flex-wrap items-center gap-x-3 gap-y-1 px-2.5 py-2.5 text-[13px] @3xl:grid @3xl:grid-cols-[1.1fr_0.8fr_1.1fr_1fr_0.9fr_1fr_0.7fr_0.6fr_0.7fr] @3xl:gap-3"
    >
      <div className="min-w-0 basis-full @3xl:basis-auto">
        <div className="flex min-w-0 items-center gap-2">
          <span className="text-theme-text min-w-0 truncate text-[13px] font-medium">
            {row.automationId}
          </span>
          <span className={`tag ${statusTone(row.status)}`}>{row.status}</span>
        </div>
      </div>
      <div className="text-theme-text text-[13px]">{row.triggerType}</div>
      <div className="min-w-0">
        <button
          type="button"
          aria-label={`Open issue ${row.identifier}`}
          onClick={() => {
            onSelectIssue(row.identifier);
          }}
          className="text-theme-text hover:text-theme-text min-w-0 truncate text-[13px] font-medium"
        >
          {row.identifier}
        </button>
        {row.title && (
          <div className="text-theme-muted mt-0.5 truncate text-[11px]" title={row.title}>
            {row.title}
          </div>
        )}
      </div>
      <div
        className={`min-w-0 flex-wrap gap-1 ${unresolved.length === 0 ? 'hidden @3xl:flex' : 'flex'}`}
      >
        {unresolved.length === 0 ? (
          <span className="text-theme-muted text-[11px]">none</span>
        ) : (
          unresolved.map((blocker) => (
            <span key={blockerLabel(blocker)} className="tag pill-warn font-mono">
              {blockerLabel(blocker)}
            </span>
          ))
        )}
      </div>
      <div className="text-theme-text flex min-w-0 flex-wrap gap-1 text-[13px]">
        <span className="truncate">{row.profile}</span>
        {row.backend ? <span className="text-theme-muted truncate">{row.backend}</span> : null}
      </div>
      <div className="min-w-0">
        <span className="text-theme-text">{reasonLabel(row)}</span>
        {row.reasonDetail && (
          <div className="text-theme-muted mt-0.5 truncate text-[11px]" title={row.reasonDetail}>
            {row.reasonDetail}
          </div>
        )}
      </div>
      <div className="text-theme-text-secondary" title={row.queuedAt}>
        {queuedAge(row.queuedAt)}
      </div>
      <div className="text-theme-text-secondary">
        {row.attemptCount === 1 ? '1 attempt' : `${String(row.attemptCount)} attempts`}
      </div>
      <div className="ml-auto @3xl:ml-0">
        <button
          type="button"
          aria-label={`Open ${row.identifier} details`}
          onClick={() => {
            onSelectQueue(row.id);
          }}
          className={DETAILS_BUTTON}
        >
          Details
        </button>
      </div>
    </div>
  );
}
