import { backendLabel } from '../../../lib/backends';
import type { Totals } from '../../../types/schemas';

function startOfTodayMs(now: number = Date.now()): number {
  const d = new Date(now);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

export function automationsFiredToday(
  rows: Array<{ automationId?: string; finishedAt: string; startedAt?: string }>,
  now: number = Date.now(),
): number {
  const start = startOfTodayMs(now);
  let count = 0;
  for (const row of rows) {
    if (!row.automationId) continue;
    const t = Date.parse(row.finishedAt);
    if (!Number.isNaN(t) && t >= start) count++;
  }
  return count;
}

/**
 * Which runs the cost estimate leaves out: the backends with runs that never
 * reported a cost (Codex, and any other that reports none). An older daemon
 * sends only the Claude / Codex counts, so it falls back to those.
 */
export function costCoverageNote(totals: Totals): string {
  const coverage = totals.costCoverage;
  const byBackend = coverage?.runsByBackend;
  if (!byBackend) {
    return (coverage?.codexRuns ?? 0) > 0 ? 'estimated, Claude runs only' : 'estimated';
  }
  const reported = new Set(coverage.costBackends ?? []);
  const left = Object.keys(byBackend)
    .filter((name) => (byBackend[name] ?? 0) > 0 && !reported.has(name))
    .sort()
    .map(backendLabel);
  return left.length === 0 ? 'estimated' : `estimated, excludes ${left.join(', ')} runs`;
}
