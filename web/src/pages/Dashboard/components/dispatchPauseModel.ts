import type { DispatchPauseRow } from '../../../types/schemas';

// #213 — the dispatch pause's capability name and chip label, shared by
// DispatchPauseToggle and its tests.

export const DISPATCH_PAUSE_CAPABILITY = 'dispatch_pause';

function formatUntil(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

export function dispatchPausedLabel(p: DispatchPauseRow): string {
  let label = 'Dispatch paused';
  if (p.reason) label += ` · ${p.reason}`;
  if (p.until) label += ` · until ${formatUntil(p.until)}`;
  return label;
}
