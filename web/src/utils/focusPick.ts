import type { StateSnapshot } from '../types/schemas';
import { inputRequiredRowState } from './inputRequired';

type FocusSnapshot = Pick<StateSnapshot, 'running' | 'history' | 'inputRequired'>;

/** Why the issue view chose an issue (shown next to its title). */
export type FocusReason = 'input_required' | 'running' | 'finished';

export interface FocusPick {
  identifier: string;
  reason: FocusReason;
}

const time = (iso: string | undefined): number => {
  const t = iso ? Date.parse(iso) : NaN;
  return Number.isFinite(t) ? t : -Infinity;
};

/** The row with the greatest `at(row)`; the earlier row wins a tie. */
function latest<T>(rows: readonly T[], at: (row: T) => number): T | undefined {
  let best: T | undefined;
  let bestAt = -Infinity;
  for (const row of rows) {
    const t = at(row);
    if (best === undefined || t > bestAt) {
      best = row;
      bestAt = t;
    }
  }
  return best;
}

/**
 * The issue `/focus` (no identifier) shows. In order:
 *   1. an issue waiting for the operator's input — the one waiting longest
 *      (earliest queuedAt); a reply already received (pending resume) does
 *      not count, nothing is left for the operator to do there;
 *   2. the most recently started running issue;
 *   3. the most recently finished run.
 * null when the snapshot has none of these (the empty state).
 */
export function pickFocusIssue(snapshot: FocusSnapshot | null | undefined): FocusPick | null {
  if (!snapshot) return null;
  const waiting = (snapshot.inputRequired ?? []).filter(
    (r) => inputRequiredRowState(r) === 'input_required',
  );
  const longest = latest(waiting, (r) => -time(r.queuedAt));
  if (longest) return { identifier: longest.identifier, reason: 'input_required' };
  const running = latest(snapshot.running, (r) => time(r.startedAt));
  if (running) return { identifier: running.identifier, reason: 'running' };
  const finished = latest(snapshot.history ?? [], (r) => time(r.finishedAt));
  if (finished) return { identifier: finished.identifier, reason: 'finished' };
  return null;
}
