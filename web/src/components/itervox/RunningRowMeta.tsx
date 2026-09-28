import type { RunningRow } from '../../types/schemas';
import { fmtAge, fmtTokens } from '../../utils/format';

/**
 * CORE-090 — the collapsed running row's "where and on what" line: backend,
 * worker host, tokens, last-activity age and profile. Lives inside the row's
 * last-event cell, so it wraps with the CORE-087 card layout instead of
 * widening the fixed grid. Absent backend → '—'; absent host → 'local' (the
 * snapshot omits workerHost for local runs); absent/unparsable lastEventAt →
 * '—'. `now` comes from one table-level ticker.
 */
export function RunningRowMeta({
  row,
  profile,
  now,
}: {
  row: RunningRow;
  profile: string | undefined;
  now: number;
}) {
  const age = fmtAge(row.lastEventAt, now);
  return (
    <span
      data-testid={`running-row-meta-${row.identifier}`}
      className="text-theme-muted flex flex-wrap items-center gap-x-2 gap-y-0.5 font-mono text-[11px]"
    >
      <span title="Backend">{row.backend || '—'}</span>
      <span aria-hidden="true">@</span>
      <span title="Host">{row.workerHost || 'local'}</span>
      <span aria-hidden="true">·</span>
      <span title={`${String(row.tokens)} tokens`}>{fmtTokens(row.tokens)} tokens</span>
      <span aria-hidden="true">·</span>
      <span title="Last activity">{age === null ? '—' : `active ${age} ago`}</span>
      {profile && (
        <>
          <span aria-hidden="true">·</span>
          <span title="Profile">{profile}</span>
        </>
      )}
    </span>
  );
}
