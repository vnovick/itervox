// One hotspot (#201): a file or directory, how often problem runs touched
// it per signal, the issues involved and links to those runs' logs.

import { Link } from 'react-router';
import { logsPath } from '../../../hooks/useUrlState';
import type { RepoHotspotRow as Row } from '../../../types/schemas';
import { fmtSeen } from '../prompts/promptVersionGroups';
import { SIGNAL_LABEL, rowCounts } from './hotspotSignals';

export function HotspotRow({ row }: { row: Row }) {
  const runs = row.runs ?? [];
  const issues = row.issues ?? [];
  return (
    <li className="border-theme-line rounded border p-3" data-path={row.path}>
      <div className="flex flex-wrap items-center gap-2">
        <code className="text-theme-text font-mono text-xs font-semibold break-all">
          {row.path}
        </code>
        <span className="text-theme-muted ml-auto text-[11px]">{row.total} problems</span>
      </div>
      <div className="mt-1 flex flex-wrap gap-1.5">
        {rowCounts(row).map((c) => (
          <span
            key={c.key}
            className="border-theme-line text-theme-text rounded border px-1.5 py-0.5 text-[10px]"
          >
            {c.n} {c.label}
          </span>
        ))}
      </div>
      <p className="text-theme-muted mt-1 text-[11px]">
        Issues:{' '}
        {issues.map((id, i) => (
          <span key={id}>
            {i > 0 && ', '}
            <Link to={logsPath(id)} className="text-theme-accent font-mono hover:underline">
              {id}
            </Link>
          </span>
        ))}
      </p>
      {runs.length > 0 && (
        <details className="mt-1">
          <summary className="text-theme-muted cursor-pointer text-[11px]">
            {runs.length} run{runs.length === 1 ? '' : 's'}
          </summary>
          <ul className="mt-1 space-y-1" aria-label={`Runs that touched ${row.path}`}>
            {runs.map((r) => (
              <li
                key={`${r.identifier}-${r.runId ?? r.finishedAt}`}
                className="flex flex-wrap items-center gap-2 text-xs"
              >
                <Link
                  to={logsPath(r.identifier)}
                  className="text-theme-accent font-mono hover:underline"
                >
                  {r.identifier}
                </Link>
                <span className="text-theme-muted">
                  {r.signals.map((s) => SIGNAL_LABEL[s]).join(', ')} · {r.kind ?? 'worker'}
                  {r.profile ? ` (${r.profile})` : ''} · {fmtSeen(r.finishedAt)}
                </span>
                {r.prUrl && (
                  <a
                    href={r.prUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="text-theme-accent ml-auto hover:underline"
                  >
                    PR
                  </a>
                )}
              </li>
            ))}
          </ul>
        </details>
      )}
    </li>
  );
}
