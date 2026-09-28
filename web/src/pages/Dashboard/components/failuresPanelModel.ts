// CORE-046 — pure helpers for FailuresPanel (kept out of the .tsx so the
// component file only exports components).
import type { FailureRow } from '../../../types/schemas';

const KIND_LABELS: Record<string, string> = {
  worker_failed: 'Worker failed',
  worker_stalled: 'Worker stalled',
  tracker_poll: 'Tracker poll',
  tracker_write: 'Tracker write',
  persist: 'Persistence',
  outbox: 'Outbox',
  panic: 'Panic',
  client: 'Dashboard',
};

/** Human label for a failure kind; unknown kinds render as-is. */
export function failureKindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind;
}

/** Newest occurrence first; ties broken by recordedAt (newest first). */
export function sortFailuresByOccurredAt(failures: readonly FailureRow[]): FailureRow[] {
  return [...failures].sort(
    (a, b) =>
      Date.parse(b.occurredAt) - Date.parse(a.occurredAt) ||
      Date.parse(b.recordedAt) - Date.parse(a.recordedAt),
  );
}
