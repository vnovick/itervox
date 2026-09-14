import { useMemo, useState } from 'react';
import { QueueSearchInput } from '../../../components/itervox/QueueSearchInput';
import type { OutboxEntryRow } from '../../../types/schemas';
import { useDiscardOutboxEntry, useRetryOutboxEntry } from '../../../queries/outbox';
import { queuedAge } from './automationQueueModel';

// OutboxList follows AutomationQueueList's pattern (write-ahead-outbox
// design, "Surfaces" / Task 4): a durable-queue panel with local search and
// per-row action buttons that call the mutation hooks directly (no
// onSelect-style callback into a parent detail panel — the outbox entry's
// full state already fits in one row, unlike an automation queue entry).
export function OutboxList({
  entries,
  onSelectIssue,
}: {
  entries: readonly OutboxEntryRow[];
  onSelectIssue: (identifier: string) => void;
}) {
  const [search, setSearch] = useState('');
  const rows = useMemo(() => filterOutboxRows(entries, search), [entries, search]);

  if (entries.length === 0) return null;

  return (
    <section
      className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-lg)] border"
      data-testid="outbox-list"
    >
      <div className="border-theme-line flex flex-col gap-3 border-b px-4 py-3">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <h2 className="text-theme-text flex items-center gap-2 text-sm font-semibold">
              Outbox
              <span className="bg-theme-bg-soft text-theme-text-secondary rounded-full px-1.5 py-0.5 text-[10px] font-bold">
                {search.trim()
                  ? `${String(rows.length)}/${String(entries.length)}`
                  : entries.length}
              </span>
            </h2>
            <p className="text-theme-text-secondary mt-0.5 text-xs">
              Tracker writes queued durably, flushed by an independent worker.
            </p>
          </div>
        </div>
        <QueueSearchInput
          value={search}
          onChange={setSearch}
          label="Search outbox"
          placeholder="Search issue, kind, error..."
          testId="outbox-search"
        />
      </div>

      {rows.length === 0 ? (
        <div className="text-theme-muted px-4 py-8 text-center text-sm">
          No matching outbox entries
        </div>
      ) : (
        <div>
          <div className="border-theme-line text-theme-muted hidden grid-cols-[1fr_0.8fr_0.9fr_0.6fr_1.2fr_0.8fr_0.9fr] gap-3 border-b px-4 py-2 text-[10px] font-semibold tracking-[0.06em] uppercase lg:grid">
            <span>Issue</span>
            <span>Kind</span>
            <span>Target</span>
            <span>Attempts</span>
            <span>Last error</span>
            <span>Queued</span>
            <span>Actions</span>
          </div>
          <div className="divide-theme-line divide-y">
            {rows.map((row) => (
              <OutboxItem key={row.id} row={row} onSelectIssue={onSelectIssue} />
            ))}
          </div>
        </div>
      )}
    </section>
  );
}

function OutboxItem({
  row,
  onSelectIssue,
}: {
  row: OutboxEntryRow;
  onSelectIssue: (identifier: string) => void;
}) {
  const retryMutation = useRetryOutboxEntry();
  const discardMutation = useDiscardOutboxEntry();

  return (
    <div
      data-testid={`outbox-card-${row.id}`}
      className="grid gap-2 px-4 py-3 lg:grid-cols-[1fr_0.8fr_0.9fr_0.6fr_1.2fr_0.8fr_0.9fr] lg:items-center lg:gap-3"
    >
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
      </div>
      <div className="text-theme-text-secondary font-mono text-[11px]">{row.kind}</div>
      <div className="text-theme-text-secondary truncate text-xs">{row.targetState ?? '—'}</div>
      <div className="text-theme-text-secondary text-xs">{row.attempts}</div>
      <div className="min-w-0">
        {row.degraded && (
          <span
            data-testid={`outbox-degraded-badge-${row.id}`}
            className="bg-theme-danger-soft text-theme-danger inline-flex rounded px-1.5 py-0.5 text-[10px] font-medium"
          >
            degraded
          </span>
        )}
        {row.lastError && (
          <div className="text-theme-muted mt-0.5 truncate text-[11px]" title={row.lastError}>
            {row.lastError}
          </div>
        )}
      </div>
      <div className="text-theme-muted font-mono text-[11px]" title={row.enqueuedAt}>
        {queuedAge(row.enqueuedAt)}
      </div>
      <div className="flex flex-wrap gap-1.5">
        <button
          type="button"
          disabled={retryMutation.isPending}
          onClick={() => {
            retryMutation.mutate(row.id);
          }}
          className="border-theme-line text-theme-accent min-h-9 rounded-[var(--radius-sm)] border px-2.5 py-1 text-[11px] font-medium hover:opacity-80 disabled:cursor-not-allowed disabled:opacity-50"
        >
          Retry
        </button>
        <button
          type="button"
          disabled={discardMutation.isPending}
          onClick={() => {
            discardMutation.mutate(row.id);
          }}
          className="border-theme-line text-theme-danger min-h-9 rounded-[var(--radius-sm)] border px-2.5 py-1 text-[11px] font-medium hover:opacity-80 disabled:cursor-not-allowed disabled:opacity-50"
        >
          Discard
        </button>
      </div>
    </div>
  );
}

function filterOutboxRows(
  entries: readonly OutboxEntryRow[],
  search: string,
): readonly OutboxEntryRow[] {
  const q = search.trim().toLowerCase();
  if (!q) return entries;
  return entries.filter((row) =>
    [row.identifier, row.kind, row.targetState, row.lastError]
      .filter((v): v is string => Boolean(v))
      .some((v) => v.toLowerCase().includes(q)),
  );
}
