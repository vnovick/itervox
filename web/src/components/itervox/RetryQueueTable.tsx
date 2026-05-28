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
  const cancelMutation = useCancelIssue();
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
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-lg)] border">
      {/* Header */}
      <div className="border-theme-line flex flex-col gap-3 border-b px-4 py-3">
        <div>
          <h2 className="text-theme-text flex items-center gap-2 text-sm font-semibold">
            Retry Queue
            <span className="bg-theme-warning-soft text-theme-warning rounded-full px-1.5 py-0.5 text-[10px] font-bold">
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
            <div
              role="button"
              tabIndex={0}
              aria-label={`Toggle details for retrying issue ${row.identifier}`}
              onClick={() => {
                toggle(row.identifier);
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') toggle(row.identifier);
              }}
              className="flex cursor-pointer flex-wrap items-center gap-2 px-4 py-3 transition-colors hover:bg-[var(--bg-soft)]"
            >
              {/* Chevron */}
              <span
                className="text-theme-muted text-[10px] transition-transform duration-200"
                style={{ transform: expandedId === row.identifier ? 'rotate(90deg)' : 'none' }}
              >
                ▶
              </span>

              {/* Identifier */}
              <span
                role="button"
                tabIndex={0}
                aria-label={`View details for retrying issue ${row.identifier}`}
                className="text-theme-accent cursor-pointer font-mono text-xs font-semibold hover:underline"
                onClick={(e) => {
                  e.stopPropagation();
                  setSelectedIdentifier(row.identifier);
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.stopPropagation();
                    setSelectedIdentifier(row.identifier);
                  }
                }}
              >
                {row.identifier}
              </span>

              {/* Attempt badge */}
              <span className="bg-theme-warning-soft text-theme-warning rounded px-1.5 py-0.5 font-mono text-[10px] font-medium">
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
                className="text-theme-danger ml-auto text-[11px] font-medium disabled:opacity-50"
              >
                {cancelling === row.identifier ? 'Cancelling…' : 'Cancel'}
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
