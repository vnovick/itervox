import type { BackendHealthRow } from '../../../types/schemas';

// CORE-055 — pure label model for BackendHealthChips (see there).

export interface BackendHealthChipModel {
  key: string;
  label: string;
  title: string;
  danger: boolean;
  /** Display name ("Codex@build-1") and the breaker it clears. */
  name: string;
  backend: string;
  host: string;
  clearable: boolean;
}

// BH-M3-9: HH:MM for a reset later today; with the date otherwise, so a
// seven_day reset never reads as today.
function resetClock(iso: string, now: number): string {
  const t = new Date(iso);
  const n = new Date(now);
  if (t.toDateString() === n.toDateString()) {
    return t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
  return t.toLocaleString([], {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

function backendName(row: BackendHealthRow): string {
  const name = row.backend.charAt(0).toUpperCase() + row.backend.slice(1);
  return row.host ? `${name}@${row.host}` : name;
}

function countsSuffix(row: BackendHealthRow): string {
  const parts: string[] = [];
  if (row.heldIssues) parts.push(`${String(row.heldIssues)} held`);
  if (row.reroutedIssues) parts.push(`${String(row.reroutedIssues)} rerouted`);
  return parts.length > 0 ? ` · ${parts.join(' · ')}` : '';
}

export function backendHealthChipModels(
  rows: readonly BackendHealthRow[] | undefined,
  now: number = Date.now(),
): BackendHealthChipModel[] {
  const out: BackendHealthChipModel[] = [];
  for (const row of rows ?? []) {
    const name = backendName(row);
    let label: string;
    let title: string;
    switch (row.status) {
      case 'limited':
        if (row.limitedUntil) {
          label = `${name} limited until ${resetClock(row.limitedUntil, now)}`;
          title = `${name} agent backend limited until ${new Date(row.limitedUntil).toLocaleString()}`;
        } else if (row.retryAt) {
          label = `${name} limited (reset unknown, retry ${resetClock(row.retryAt, now)})`;
          title = `${name} agent backend limited; the vendor published no reset time`;
        } else {
          label = `${name} limited (reset unknown)`;
          title = `${name} agent backend limited; the vendor published no reset time`;
        }
        break;
      case 'probing':
        label = `${name} probing${row.probeIssue ? ` (${row.probeIssue})` : ''}`;
        title = `${name} limit expired; one probe run decides whether the backend is back`;
        break;
      case 'warning':
        label = `${name} retrying rate limits`;
        title = `${name} is retrying rate-limited API calls; not limited yet`;
        break;
      default:
        continue;
    }
    if (row.kind) title += ` (${row.kind}${row.limitType ? `, ${row.limitType}` : ''})`;
    out.push({
      key: `${row.backend}@${row.host ?? ''}`,
      label: label + countsSuffix(row),
      title,
      danger: row.status === 'limited',
      name,
      backend: row.backend,
      host: row.host ?? '',
      clearable: row.status === 'limited' || row.status === 'probing',
    });
  }
  return out;
}
