import { useMemo } from 'react';
import type { FailureRow } from '../../../types/schemas';
import { failureKindLabel, sortFailuresByOccurredAt } from './failuresPanelModel';

// CORE-046 — the daemon's RecentFailures ring (worker failures and stalls,
// tracker poll/write failures, persistence errors, outbox delivery failures,
// recovered panics, web client errors) as one scannable list. Messages are
// redacted daemon-side. The ring is ordered by recordedAt; the panel sorts by
// occurredAt because off-loop producers can deliver an older failure late.

function failureKey(f: FailureRow): string {
  return `${f.recordedAt}|${f.kind}|${f.identifier ?? ''}|${f.source ?? ''}|${f.message}`;
}

function formatTime(iso: string): string {
  const ms = Date.parse(iso);
  if (Number.isNaN(ms)) return iso;
  return new Date(ms).toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

export function FailuresPanel({
  failures,
  onSelectIssue,
}: {
  failures: readonly FailureRow[];
  onSelectIssue: (identifier: string) => void;
}) {
  const rows = useMemo(() => sortFailuresByOccurredAt(failures), [failures]);
  if (failures.length === 0) return null;

  return (
    <section
      aria-labelledby="recent-failures-heading"
      className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-lg)] border"
      data-testid="failures-panel"
    >
      <div className="border-theme-line border-b px-4 py-3">
        <h2
          id="recent-failures-heading"
          className="text-theme-text flex items-center gap-2 text-sm font-semibold"
        >
          Recent failures
          <span className="bg-theme-danger-soft text-theme-danger-text rounded-full px-1.5 py-0.5 text-[10px] font-bold">
            {failures.length}
          </span>
        </h2>
        <p className="text-theme-text-secondary mt-0.5 text-xs">
          The daemon&apos;s last {failures.length === 1 ? 'failure' : 'failures'} this session,
          newest first. Details are in the issue log and the daemon log.
        </p>
      </div>
      <ul className="divide-theme-line max-h-80 divide-y overflow-y-auto" aria-live="polite">
        {rows.map((f) => (
          <li key={failureKey(f)} className="flex flex-col gap-1 px-4 py-2 text-xs">
            <div className="flex flex-wrap items-center gap-2">
              <span className="bg-theme-danger-soft text-theme-danger-text rounded px-1.5 py-0.5 font-medium">
                {failureKindLabel(f.kind)}
              </span>
              {f.identifier && (
                <button
                  type="button"
                  aria-label={`Open ${f.identifier}`}
                  onClick={() => {
                    onSelectIssue(f.identifier ?? '');
                  }}
                  className="text-theme-accent-text font-mono hover:underline"
                >
                  {f.identifier}
                </button>
              )}
              {f.source && <span className="text-theme-muted font-mono">{f.source}</span>}
              {f.count > 1 && (
                <span
                  aria-label={`repeated ${String(f.count)} times`}
                  className="bg-theme-bg-soft text-theme-text-secondary rounded-full px-1.5 py-0.5 text-[10px] font-bold"
                >
                  ×{f.count}
                </span>
              )}
              <time dateTime={f.occurredAt} className="text-theme-muted ml-auto">
                {formatTime(f.occurredAt)}
              </time>
            </div>
            <p className="text-theme-text-secondary font-mono break-words">{f.message}</p>
          </li>
        ))}
      </ul>
    </section>
  );
}
