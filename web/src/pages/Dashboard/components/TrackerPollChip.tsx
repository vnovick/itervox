import type { TrackerPollRow } from '../../../types/schemas';
import { OpsChip } from './OpsChip';
import { compactDuration } from './SyncStatusChip';

export interface TrackerPollChipModel {
  label: string;
  // danger: the tracker breaker has the poll degraded (reads paused).
  // Otherwise the chip is a warning: a poll running (or last run) past the
  // daemon's slow threshold.
  danger: boolean;
  title: string;
}

function localTime(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// runningFor is how long the in-flight poll has run at `now`, recomputed on
// the dashboard's own clock so it keeps growing between snapshot pushes.
function runningFor(row: TrackerPollRow, now: number): number {
  if (row.startedAt) {
    const started = new Date(row.startedAt).getTime();
    if (Number.isFinite(started)) return Math.max(0, now - started);
  }
  return row.runningForMs ?? 0;
}

// trackerPollChipModel derives the tracker poll chip (#187), or null when the
// poll is healthy (compact when healthy) or the daemon predates the field.
export function trackerPollChipModel(
  row: TrackerPollRow | undefined,
  now: number,
): TrackerPollChipModel | null {
  if (!row) return null;
  if (row.degraded) {
    const failures = row.consecutiveTransportFailures ?? 0;
    return {
      label: row.breakerOpenUntil
        ? `Tracker degraded · reads paused until ${localTime(row.breakerOpenUntil)}`
        : 'Tracker degraded',
      danger: true,
      title:
        `The tracker timed out or was unreachable on ${String(failures)} polls in a row, so ` +
        'itervox skips tracker reads for a cooldown and then probes it again. ' +
        'Workers, the dashboard and operator actions keep working meanwhile.',
    };
  }
  if (row.inFlight) {
    const ms = runningFor(row, now);
    if (ms <= row.slowAfterMs) return null;
    return {
      label: `Tracker poll running ${compactDuration(ms)}`,
      danger: false,
      title:
        `The tracker poll has been running for ${compactDuration(ms)} ` +
        `(slow after ${compactDuration(row.slowAfterMs)}). It runs off the event loop, ` +
        'so workers, the dashboard and operator actions keep working.',
    };
  }
  if (row.slow) {
    return {
      label: `Tracker poll slow · last ${compactDuration(row.lastDurationMs)}`,
      danger: false,
      title: `The last tracker poll took ${compactDuration(row.lastDurationMs)} (slow after ${compactDuration(row.slowAfterMs)}).`,
    };
  }
  return null;
}

// TrackerPollChip renders the tracker poll tile (#187): a poll running long,
// or degraded by the tracker breaker. Presentational — takes the snapshot
// slice and the coarse clock as props so its logic is testable without a
// store.
export function TrackerPollChip({ row, now }: { row: TrackerPollRow | undefined; now: number }) {
  const model = trackerPollChipModel(row, now);
  if (!model) return null;
  return (
    <OpsChip
      testId="tracker-poll-chip"
      label={model.label}
      warning={!model.danger}
      danger={model.danger}
      title={model.title}
    />
  );
}
