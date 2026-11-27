import type { NeedsYouRow } from '../types/schemas';

// The "Needs you" lane's view model: how long an item has waited and how
// its kind reads. The daemon sends the rows oldest first
// (internal/server/needs_you.go); nothing here re-sorts them.

const MINUTE_MS = 60_000;
const HOUR_MS = 60 * MINUTE_MS;
const DAY_MS = 24 * HOUR_MS;

/**
 * A wait as the lane shows it: "under 1m", "14m", "2h 14m", "3d 4h". An
 * unknown or negative wait (clock skew) is undefined.
 */
export function formatWait(ms: number): string | undefined {
  if (!Number.isFinite(ms) || ms < 0) return undefined;
  if (ms < MINUTE_MS) return 'under 1m';
  if (ms < HOUR_MS) return `${String(Math.floor(ms / MINUTE_MS))}m`;
  if (ms < DAY_MS) {
    return `${String(Math.floor(ms / HOUR_MS))}h ${String(Math.floor((ms % HOUR_MS) / MINUTE_MS))}m`;
  }
  return `${String(Math.floor(ms / DAY_MS))}d ${String(Math.floor((ms % DAY_MS) / HOUR_MS))}h`;
}

/** The wait of `row` at `now` (ms since the epoch); undefined when unknown. */
export function waitOf(row: Pick<NeedsYouRow, 'waitingSince'>, now: number): string | undefined {
  if (!row.waitingSince) return undefined;
  const since = Date.parse(row.waitingSince);
  return Number.isNaN(since) ? undefined : formatWait(now - since);
}

export const NEEDS_YOU_KIND_LABEL: Record<NeedsYouRow['kind'], string> = {
  question: 'Question',
  approval: 'Approval',
  'pipeline-change': 'Pipeline change',
};

/** Whether the item is decided with Approve / Reject (not a free reply). */
export function isDecision(kind: NeedsYouRow['kind']): boolean {
  return kind === 'approval' || kind === 'pipeline-change';
}
