import { useMemo, useState } from 'react';
import { QueueSearchInput } from '../../../components/itervox/QueueSearchInput';
import type {
  AutomationQueueBackpressure,
  AutomationQueueRow,
  DependencyAuditRow,
  RunningRow,
} from '../../../types/schemas';
import { AutomationQueueItem } from './AutomationQueueItem';
import { AutomationRunningItem } from './AutomationRunningItem';
import {
  dependencyMap,
  filterQueueRows,
  filterRunningAutomations,
  sortAutomationQueueRows,
} from './automationQueueModel';

export { sortAutomationQueueRows };

// Most automations dispatch immediately (slots free,
// no blockers) and never enter `state.AutomationQueue`. Surfacing them
// alongside the queued rows in the same panel matches the operator mental
// model that "the automation panel shows what my automations did."
export function AutomationQueueList({
  queue,
  running,
  dependencyAudit,
  backpressure,
  onSelectIssue,
  onSelectQueue,
}: {
  queue: readonly AutomationQueueRow[];
  running?: readonly RunningRow[];
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
  const runningAutomations = useMemo(
    () => filterRunningAutomations(running, search),
    [running, search],
  );
  const saturated = Boolean(backpressure?.saturated || backpressure?.pausedProducers);

  return (
    <section
      aria-labelledby="automation-queue-heading"
      className="glass-panel @container flex flex-col gap-2 overflow-hidden px-3 py-4 sm:px-[18px]"
    >
      <div className="flex flex-col gap-3 px-1">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <h2 id="automation-queue-heading" className="section-title flex items-center gap-2">
              Automation Queue
              <span className="count-pill">
                {search.trim() ? `${String(rows.length)}/${String(queue.length)}` : queue.length}
              </span>
            </h2>
            <p className="text-theme-text-secondary mt-1 text-[13px]">
              Triggered automations waiting for worker capacity or dependencies.
            </p>
          </div>
        </div>
        {(queue.length > 0 || runningAutomations.length > 0) && (
          // codex-B7: render the search input when EITHER queued rows OR
          // running automations are present, so the operator can filter the
          // running section even before anything has queued.
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
        <div role="alert" className="banner-danger">
          <strong>Automation intake paused:</strong> queue is full at {backpressure.length}/
          {backpressure.maxLength}. Existing queued automations will continue draining as workers
          free up.
        </div>
      )}

      {runningAutomations.length > 0 && (
        <div data-testid="automation-running-section">
          <div className="table-head flex items-center gap-2 px-2.5 py-2">
            <span className="tag pill-ok">Running</span>
            <span>{runningAutomations.length} active</span>
          </div>
          <div className="divide-theme-line border-theme-line divide-y border-t">
            {runningAutomations.map((row) => (
              <AutomationRunningItem
                // codex-B6: include a row-source discriminator so a live
                // session that briefly coexists with the same id in the
                // history snapshot does not produce a React duplicate-key
                // warning. "running" makes the dedup explicit.
                key={`${row.sessionId ?? `${row.identifier}-${row.startedAt}`}-running`}
                row={row}
                onSelectIssue={onSelectIssue}
              />
            ))}
          </div>
        </div>
      )}

      {queue.length === 0 && runningAutomations.length === 0 ? (
        <div className="text-theme-muted px-4 py-6 text-center text-sm">
          No automation queue items
        </div>
      ) : queue.length === 0 ? null : rows.length === 0 ? (
        <div className="text-theme-muted px-4 py-6 text-center text-sm">
          No matching automation queue items
        </div>
      ) : (
        <div>
          <div className="table-head hidden grid-cols-[1.1fr_0.8fr_1.1fr_1fr_0.9fr_1fr_0.7fr_0.6fr_0.7fr] gap-3 px-2.5 py-2 @3xl:grid">
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
          <div className="divide-theme-line border-theme-line divide-y border-t">
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
