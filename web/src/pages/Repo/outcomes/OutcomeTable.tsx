// One outcomes table (cost and outcome visibility): per profile or per
// playbook, runs, success rate, PRs merged (of PRs), median wall time and
// tokens, and retry rate over the window.

import type { OutcomeGroupRow } from '../../../types/schemas';
import { fmtMs, fmtTokens } from '../../../utils/format';
import { fmtRate } from '../prompts/promptVersionGroups';

interface OutcomeTableProps {
  title: string;
  /** Label for the row with an empty name (default profile, no playbook). */
  emptyName: string;
  rows: readonly OutcomeGroupRow[];
}

function prSummary(r: OutcomeGroupRow): string {
  if (r.prs === 0) return '—';
  const extra = [
    r.prsClosed > 0 ? `${String(r.prsClosed)} closed` : '',
    r.prsReverted > 0 ? `${String(r.prsReverted)} reverted` : '',
  ].filter(Boolean);
  return `${String(r.prsMerged)} / ${String(r.prs)}${extra.length ? ` (${extra.join(', ')})` : ''}`;
}

export function OutcomeTable({ title, emptyName, rows }: OutcomeTableProps) {
  return (
    <div className="glass-panel px-5 py-[18px]">
      <p className="section-title mb-2">{title}</p>
      {rows.length === 0 ? (
        <p className="text-theme-muted text-sm">No runs in this window.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="data-table min-w-[40rem]" aria-label={`Outcomes by ${title}`}>
            <thead>
              <tr>
                <th scope="col">{title}</th>
                <th scope="col" className="text-right">
                  Runs
                </th>
                <th scope="col" className="text-right">
                  Success
                </th>
                <th scope="col" className="text-right">
                  PRs merged
                </th>
                <th scope="col" className="text-right">
                  Median time
                </th>
                <th scope="col" className="text-right">
                  Median tokens
                </th>
                <th scope="col" className="text-right">
                  Retry rate
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${title}-${r.name}`}>
                  <td className="font-mono">{r.name || emptyName}</td>
                  <td className="text-right tabular-nums">{r.runs}</td>
                  <td className="text-right tabular-nums">
                    {r.finished > 0 ? fmtRate(r.successRate) : '—'}
                  </td>
                  <td className="text-right tabular-nums">{prSummary(r)}</td>
                  <td className="text-theme-text-secondary text-right tabular-nums">
                    {fmtMs(r.medianElapsedMs)}
                  </td>
                  <td className="text-theme-text-secondary text-right tabular-nums">
                    {fmtTokens(r.medianTokens)}
                  </td>
                  <td className="text-right tabular-nums">{fmtRate(r.retryRate)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
