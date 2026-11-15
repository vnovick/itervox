// Hotspot signal labels (#201), in the order rows show their counts.

import type { RepoHotspotRow, RepoHotspotRun } from '../../../types/schemas';

type Signal = RepoHotspotRun['signals'][number];

export const SIGNAL_LABEL: Record<Signal, string> = {
  failed: 'failed',
  stalled: 'stalled',
  change_request: 'change requested',
  input_request: 'input requested',
  reverted: 'PR reverted',
};

/** The non-zero per-signal counts of a row, labelled. */
export function rowCounts(row: RepoHotspotRow): { key: Signal; label: string; n: number }[] {
  const counts: [Signal, number][] = [
    ['failed', row.failed],
    ['stalled', row.stalled],
    ['change_request', row.changeRequests],
    ['input_request', row.inputRequests],
    ['reverted', row.reverted],
  ];
  return counts.filter(([, n]) => n > 0).map(([key, n]) => ({ key, label: SIGNAL_LABEL[key], n }));
}
