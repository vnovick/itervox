import { useMemo, useState } from 'react';
import { QueueSearchInput } from '../../../components/itervox/QueueSearchInput';
import type {
  AutomationQueueBackpressure,
  AutomationQueueRow,
  DependencyAuditRow,
} from '../../../types/schemas';
import { AutomationQueueItem } from './AutomationQueueItem';
import { dependencyMap, filterQueueRows, sortAutomationQueueRows } from './automationQueueModel';

export { sortAutomationQueueRows };

export function AutomationQueueList({
  queue,
  dependencyAudit,
  backpressure,
  onSelectIssue,
  onSelectQueue,
}: {
  queue: readonly AutomationQueueRow[];
  dependencyAudit: readonly DependencyAuditRow[];
  backpressure?: AutomationQueueBackpressure;
  onSelectIssue: (identifier: string) => void;
  onSelectQueue: (queueId: string) => void;
}) {
  const [search, setSearch] = useState('');
  const dependencies = useMemo(() => dependencyMap(dependencyAudit), [dependencyAudit]);
  const rows = useMemo(
    () => filterQueueRows(queue, dependencies, search),
    [queue, dependencies, search],
  );
  const saturated = Boolean(backpressure?.saturated || backpressure?.pausedProducers);

  return (
    <section className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-lg)] border">
      <div className="border-theme-line flex flex-col gap-3 border-b px-4 py-3">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <h2 className="text-theme-text flex items-center gap-2 text-sm font-semibold">
              Automation Queue
              <span className="bg-theme-bg-soft text-theme-text-secondary rounded-full px-1.5 py-0.5 text-[10px] font-bold">
                {search.trim() ? `${String(rows.length)}/${String(queue.length)}` : queue.length}
              </span>
            </h2>
            <p className="text-theme-text-secondary mt-0.5 text-xs">
              Triggered automations waiting for worker capacity or dependencies.
            </p>
          </div>
        </div>
        {queue.length > 0 && (
          <QueueSearchInput
            value={search}
            onChange={setSearch}
            label="Search automation queue"
            placeholder="Search automation, issue, profile, reason, dependency..."
            testId="automation-queue-search"
          />
        )}
      </div>

      {saturated && backpressure && (
        <div
          role="alert"
          className="border-theme-danger-soft bg-theme-danger-soft text-theme-danger border-b px-4 py-3 text-sm"
        >
          <strong>Automation intake paused:</strong> queue is full at {backpressure.length}/
          {backpressure.maxLength}. Existing queued automations will continue draining as workers
          free up.
        </div>
      )}

      {queue.length === 0 ? (
        <div className="text-theme-muted px-4 py-8 text-center text-sm">
          No automation queue items
        </div>
      ) : rows.length === 0 ? (
        <div className="text-theme-muted px-4 py-8 text-center text-sm">
          No matching automation queue items
        </div>
      ) : (
        <div>
          <div className="border-theme-line text-theme-muted hidden grid-cols-[1.1fr_0.8fr_1.1fr_1fr_0.9fr_1fr_0.7fr_0.6fr_0.7fr] gap-3 border-b px-4 py-2 text-[10px] font-semibold tracking-[0.06em] uppercase lg:grid">
            <span>Automation</span>
            <span>Trigger</span>
            <span>Issue</span>
            <span>Dependencies</span>
            <span>Profile/backend</span>
            <span>Reason</span>
            <span>Queued</span>
            <span>Attempts</span>
            <span>Next</span>
          </div>
          <div className="divide-theme-line divide-y">
            {rows.map((row) => (
              <AutomationQueueItem
                key={row.id}
                row={row}
                dependency={dependencies.get(row.identifier)}
                onSelectIssue={onSelectIssue}
                onSelectQueue={onSelectQueue}
              />
            ))}
          </div>
        </div>
      )}
    </section>
  );
}
