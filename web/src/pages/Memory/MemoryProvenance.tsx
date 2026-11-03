// Memory page provenance (#202): where an entry came from, its usage, and
// the recent runs whose prompt carried it.

import type { MemoryEntry } from '../../types/schemas';

const SECTION = 'text-theme-muted text-[10px] font-bold tracking-[0.08em] uppercase';

function when(iso?: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function MemoryProvenance({ entry }: { entry: MemoryEntry }) {
  const src = entry.source;
  return (
    <div className="space-y-3">
      <section aria-label="Provenance">
        <h3 className={SECTION}>Provenance</h3>
        <dl className="mt-1 grid grid-cols-[7rem_minmax(0,1fr)] gap-x-2 gap-y-0.5 text-xs">
          <dt className="text-theme-muted">Source</dt>
          <dd>{src?.kind ?? '—'}</dd>
          {src?.issue && (
            <>
              <dt className="text-theme-muted">Issue</dt>
              <dd>{src.issue}</dd>
            </>
          )}
          {(src?.runIds ?? []).length > 0 && (
            <>
              <dt className="text-theme-muted">From runs</dt>
              <dd className="break-all">{src?.runIds?.join(', ')}</dd>
            </>
          )}
          {(src?.links ?? []).map((link) => (
            <span key={link} className="contents">
              <dt className="text-theme-muted">Link</dt>
              <dd className="break-all">
                <a
                  className="text-theme-accent-strong underline"
                  href={link}
                  target="_blank"
                  rel="noreferrer"
                >
                  {link}
                </a>
              </dd>
            </span>
          ))}
          <dt className="text-theme-muted">Created</dt>
          <dd>{when(entry.created)}</dd>
          <dt className="text-theme-muted">Updated</dt>
          <dd>{when(entry.updated)}</dd>
          <dt className="text-theme-muted">Last used</dt>
          <dd>{when(entry.lastUsed)}</dd>
          <dt className="text-theme-muted">Used by</dt>
          <dd>{entry.useCount} runs</dd>
          {entry.tags.length > 0 && (
            <>
              <dt className="text-theme-muted">Tags</dt>
              <dd>{entry.tags.join(', ')}</dd>
            </>
          )}
          <dt className="text-theme-muted">Profiles</dt>
          <dd>{entry.profiles.length > 0 ? entry.profiles.join(', ') : 'every profile'}</dd>
        </dl>
      </section>
      <section aria-label="Runs that used it">
        <h3 className={SECTION}>Runs that used it</h3>
        {entry.runs.length === 0 ? (
          <p className="text-theme-muted mt-1 text-xs">No run in the recent history.</p>
        ) : (
          <ul className="mt-1 space-y-0.5 text-xs">
            {entry.runs.map((r) => (
              <li key={`${r.runId ?? ''}-${r.identifier}-${r.finishedAt}`}>
                <span className="font-medium">{r.identifier}</span> · {r.status} ·{' '}
                {when(r.finishedAt)}
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
