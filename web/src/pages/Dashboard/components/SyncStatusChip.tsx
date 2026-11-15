import type { SyncStatusRow } from '../../../types/schemas';
import { OpsChip } from './OpsChip';

export interface SyncStatusChipModel {
  label: string;
  // warning: the pull lag is past the daemon's threshold, or the last pull
  // failed. danger: both — the board is running on old tracker data and the
  // tracker is not answering.
  warning: boolean;
  danger: boolean;
  title: string;
  stale: boolean;
}

// compactDuration renders a lag as "42s" / "12m" / "3h" / "2d".
export function compactDuration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${String(s)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${String(m)}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${String(h)}h`;
  return `${String(Math.floor(h / 24))}d`;
}

function localTime(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// lagAt is the pull lag at `now`, recomputed on the dashboard's own clock so
// it keeps growing between snapshot pushes (a quiet daemon pushes nothing).
// Falls back to the snapshot's lagMs when lastPullAt does not parse.
function lagAt(row: SyncStatusRow, now: number): number | null {
  if (!row.lastPullAt) return null;
  const pulled = new Date(row.lastPullAt).getTime();
  if (Number.isFinite(pulled)) return Math.max(0, now - pulled);
  return row.lagMs ?? null;
}

function pendingDetail(row: SyncStatusRow): string {
  const parts = [
    row.pendingUrgent > 0 ? `${String(row.pendingUrgent)} urgent` : '',
    row.pendingBatched > 0 ? `${String(row.pendingBatched)} batched` : '',
    row.pendingRetrying > 0 ? `${String(row.pendingRetrying)} retrying` : '',
    row.pendingRateLimited > 0 ? `${String(row.pendingRateLimited)} rate limited` : '',
  ].filter(Boolean);
  return parts.length > 0 ? ` (${parts.join(', ')})` : '';
}

// syncStatusChipModel derives the sync chip (#117), or null when the daemon
// predates the field.
export function syncStatusChipModel(
  row: SyncStatusRow | undefined,
  now: number,
): SyncStatusChipModel | null {
  if (!row) return null;
  const lag = lagAt(row, now);
  const stale = lag === null ? row.stale : lag > row.lagWarnAfterMs;
  const failing = row.lastError !== undefined;

  let label =
    lag === null
      ? 'Sync: no pull yet'
      : stale && row.lastPullAt
        ? `Sync lag ${compactDuration(lag)} · last pull ${localTime(row.lastPullAt)}`
        : `Synced ${compactDuration(lag)} ago`;
  if (row.pendingWrites > 0) label += ` · ${String(row.pendingWrites)} to push`;

  const title = [
    row.lastPullAt
      ? `Last pull from the tracker: ${new Date(row.lastPullAt).toLocaleString()}`
      : 'No successful pull from the tracker yet',
    lag !== null
      ? `Lag ${compactDuration(lag)} (warns after ${compactDuration(row.lagWarnAfterMs)})`
      : '',
    modeLine(row),
    row.pendingWrites > 0
      ? `${String(row.pendingWrites)} tracker write${row.pendingWrites === 1 ? '' : 's'} pending${pendingDetail(row)}` +
        (row.nextPushAt ? ` · next push ${localTime(row.nextPushAt)}` : '')
      : 'No tracker writes pending',
    row.lastError
      ? `Last sync error (${row.lastError.kind}, ${String(row.consecutiveFailures ?? 1)} in a row): ${row.lastError.message}`
      : '',
  ]
    .filter(Boolean)
    .join('\n');

  return { label, warning: stale || failing, danger: stale && failing, title, stale };
}

// modeLine describes how the board gets its tracker data: the store, direct
// polling, or (#118) local issue files synced with Linear or GitHub.
function modeLine(row: SyncStatusRow): string {
  const every = compactDuration(row.syncIntervalMs);
  if (row.mode === 'store') return `Local tracker store, pulls every ${every}`;
  if (row.mode === 'local_sync')
    return `Local issue files synced with the remote tracker every ${every}`;
  return `Direct polling every ${every}`;
}

// SyncStatusChip renders the tracker sync tile (#117): last pull, lag and
// pending writes. Presentational — takes the snapshot slice and the coarse
// clock as props so its logic is testable without a store.
export function SyncStatusChip({ row, now }: { row: SyncStatusRow | undefined; now: number }) {
  const model = syncStatusChipModel(row, now);
  if (!model) return null;
  return (
    <OpsChip
      testId="sync-status-chip"
      label={model.label}
      warning={model.warning && !model.danger}
      danger={model.danger}
      title={model.title}
    />
  );
}
