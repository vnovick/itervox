import type { IssueStatusChange } from '../../types/schemas';

type IssueStatusChangesProps = {
  changes?: readonly IssueStatusChange[];
};

const SOURCE_TONES: Record<string, string> = {
  automation: 'bg-theme-accent-soft text-theme-accent-strong',
  dashboard: 'bg-theme-bg-soft text-theme-text-secondary',
  system: 'bg-theme-bg-soft text-theme-muted',
  tracker_observed: 'bg-theme-success-soft text-theme-success',
  worker_lifecycle: 'bg-theme-warning-soft text-theme-warning',
};

function formatStatusTime(at: string): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return at;
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(d);
}

function metaChips(change: IssueStatusChange): string[] {
  return [
    change.automationId,
    change.triggerType,
    change.profileName,
    change.backend,
    change.workerHost,
  ].filter((v): v is string => Boolean(v));
}

function sourceTone(source: string): string {
  return SOURCE_TONES[source] ?? 'bg-theme-bg-soft text-theme-text-secondary';
}

export function IssueStatusChanges({ changes }: IssueStatusChangesProps) {
  if (!changes || changes.length === 0) return null;

  return (
    <section className="border-theme-line bg-theme-bg-soft rounded-[var(--radius-sm)] border p-3">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-theme-text text-xs font-medium tracking-wider uppercase">
          Status changes
        </h4>
        <span className="text-theme-muted text-xs">{changes.length} changes</span>
      </div>
      <ol className="space-y-3">
        {changes.map((change) => {
          const chips = metaChips(change);
          // v0.2.0 audit P3-3 — drop the `idx` segment that defeated React
          // reconciliation when entries were reordered. The composite of
          // (at + source + toState + automationId) is unique-enough for the
          // current emit semantics; if two entries collide on those four
          // fields they are semantically identical and reuse is correct.
          return (
            <li
              key={`${change.at}-${change.source}-${change.toState}-${change.automationId ?? ''}`}
              className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]"
            >
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2 text-sm">
                  <span className="text-theme-text min-w-0 font-medium break-words">
                    {change.fromState || 'Unknown'}
                  </span>
                  <span className="text-theme-muted font-mono text-xs">-&gt;</span>
                  <span className="text-theme-text min-w-0 font-semibold break-words">
                    {change.toState}
                  </span>
                </div>
                <div className="mt-1 flex flex-wrap gap-1.5">
                  <span
                    className={`rounded-full px-2 py-0.5 text-[10px] font-medium ${sourceTone(
                      change.source,
                    )}`}
                  >
                    {change.source}
                  </span>
                  {chips.map((chip) => (
                    <span
                      key={chip}
                      className="bg-theme-bg text-theme-text-secondary max-w-full min-w-0 rounded-full px-2 py-0.5 text-[10px] font-medium break-all"
                    >
                      {chip}
                    </span>
                  ))}
                </div>
              </div>
              <time
                dateTime={change.at}
                className="text-theme-muted self-start font-mono text-xs whitespace-nowrap"
              >
                {formatStatusTime(change.at)}
              </time>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
