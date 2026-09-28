import { useMemo, useState, useCallback } from 'react';
import { useItervoxStore } from '../../store/itervoxStore';
import { useCancelIssue } from '../../queries/issues';
import { SessionAccordion } from './SessionAccordion';
import { EMPTY_RETRYING } from '../../utils/constants';
import { QueueSearchInput } from './QueueSearchInput';

function fmtDueAt(dueAt: string): string {
  const diff = new Date(dueAt).getTime() - Date.now();
  const abs = Math.abs(diff);
  const secs = Math.round(abs / 1000);
  const mins = Math.round(abs / 60_000);
  const label = abs < 60_000 ? `${String(secs)}s` : `${String(mins)}m`;
  return diff > 0 ? `in ${label}` : `${label} ago`;
}

export default function RetryQueueTable() {
  const retrying = useItervoxStore((s) => s.snapshot?.retrying ?? EMPTY_RETRYING);
  const setSelectedIdentifier = useItervoxStore((s) => s.setSelectedIdentifier);
  const cancelMutation = useCancelIssue({ verb: 'Cancel retry' });
  const [cancelling, setCancelling] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [search, setSearch] = useState('');

  const handleCancel = (e: React.MouseEvent, identifier: string) => {
    e.stopPropagation();
    if (cancelling) return;
    setCancelling(identifier);
    cancelMutation.mutate(identifier, {
      onSettled: () => {
        setCancelling(null);
      },
    });
  };

  const toggle = useCallback((id: string) => {
    setExpandedId((prev) => (prev === id ? null : id));
  }, []);

  const q = search.trim().toLowerCase();
  const filtered = useMemo(
    () =>
      q === ''
        ? retrying
        : retrying.filter((row) =>
            [row.identifier, row.error, row.dueAt, `attempt ${String(row.attempt)}`]
              .filter(Boolean)
              .join(' ')
              .toLowerCase()
              .includes(q),
          ),
    [q, retrying],
  );

  if (retrying.length === 0) return null;

  return (
    <div
      id="retry-queue"
      className="border-theme-line bg-theme-bg-elevated scroll-mt-20 overflow-hidden rounded-[var(--radius-lg)] border"
    >
      {/* Header */}
      <div className="border-theme-line flex flex-col gap-3 border-b px-4 py-3">
        <div>
          <h2 className="text-theme-text flex items-center gap-2 text-sm font-semibold">
            Retry Queue
            <span className="bg-theme-warning-soft text-theme-warning-text rounded-full px-1.5 py-0.5 text-[10px] font-bold">
              {q ? `${String(filtered.length)}/${String(retrying.length)}` : retrying.length}
            </span>
          </h2>
          <p className="text-theme-text-secondary mt-0.5 text-xs">
            Issues waiting to be re-dispatched after a failure
          </p>
        </div>
        <QueueSearchInput
          value={search}
          onChange={setSearch}
          label="Search retry queue"
          placeholder="Search retry issue, reason, or attempt..."
        />
      </div>

      {/* Rows */}
      {filtered.length === 0 ? (
        <div className="text-theme-muted px-4 py-8 text-center text-sm">
          No matching retry queue items
        </div>
      ) : (
        filtered.map((row) => (
          <div key={row.identifier} className="border-theme-line border-b last:border-b-0">
            {/* Row — a mouse click toggles the accordion; the chevron button
                is the keyboard toggle (M5-close: no nested interactive). */}
            {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions -- mouse shortcut only; the chevron button is the keyboard toggle (M5-close) */}
            <div
              data-testid={`retry-row-${row.identifier}`}
              onClick={() => {
                toggle(row.identifier);
              }}
              className="flex cursor-pointer flex-wrap items-center gap-2 px-4 py-3 transition-colors hover:bg-[var(--bg-soft)]"
            >
              {/* Chevron — the accordion toggle */}
              <button
                type="button"
                aria-label={`Toggle details for retrying issue ${row.identifier}`}
                aria-expanded={expandedId === row.identifier}
                onClick={(e) => {
                  e.stopPropagation();
                  toggle(row.identifier);
                }}
                className="text-theme-muted inline-flex h-6 w-6 items-center justify-center rounded text-[10px]"
              >
                <span
                  aria-hidden="true"
                  className="inline-block transition-transform duration-200"
                  style={{ transform: expandedId === row.identifier ? 'rotate(90deg)' : 'none' }}
                >
                  ▶
                </span>
              </button>

              {/* Identifier — opens the detail slide */}
              <button
                type="button"
                aria-label={`View details for retrying issue ${row.identifier}`}
                className="text-theme-accent-text min-h-6 cursor-pointer font-mono text-xs font-semibold hover:underline"
                onClick={(e) => {
                  e.stopPropagation();
                  setSelectedIdentifier(row.identifier);
                }}
              >
                {row.identifier}
              </button>

              {/* Attempt badge */}
              <span className="bg-theme-warning-soft text-theme-warning-text rounded px-1.5 py-0.5 font-mono text-[10px] font-medium">
                #{row.attempt}
              </span>

              {/* Due at */}
              <span className="text-theme-text-secondary font-mono text-[11px]">
                {fmtDueAt(row.dueAt)}
              </span>

              {/* Error — truncated, hidden on mobile */}
              {row.error && (
                <span
                  className="text-theme-muted hidden min-w-0 flex-1 truncate text-xs sm:inline"
                  title={row.error}
                >
                  {row.error}
                </span>
              )}

              {/* Cancel button */}
              <button
                onClick={(e) => {
                  handleCancel(e, row.identifier);
                }}
                disabled={cancelling === row.identifier}
                className="text-theme-danger-text ml-auto min-h-6 px-1 text-[11px] font-medium disabled:opacity-50"
              >
                {cancelling === row.identifier ? 'Cancelling retry…' : '✕ Cancel retry'}
              </button>
            </div>

            {/* Expandable accordion */}
            {expandedId === row.identifier && (
              <SessionAccordion
                identifier={row.identifier}
                workerHost={undefined}
                sessionId={undefined}
              />
            )}
          </div>
        ))
      )}
    </div>
  );
}
