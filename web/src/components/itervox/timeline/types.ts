import type { HistoryRow, IssueLogEntry, RunningRow } from '../../../types/schemas';

// ─── Helpers ──────────────────────────────────────────────────────────────────

export const clamp01 = (x: number) => Math.max(0, Math.min(1, x));

// ─── Data model ───────────────────────────────────────────────────────────────

export interface NormalisedSession {
  identifier: string;
  title?: string;
  startedAt: string;
  finishedAt?: string;
  elapsedMs: number;
  turnCount: number;
  tokens: number;
  status: 'live' | 'succeeded' | 'failed' | 'cancelled' | 'stalled' | 'input_required';
  sessionId?: string;
  // Automation context (T-5). Populated by F-1 when the run was dispatched
  // by an automation rule; undefined for manual runs. The Timeline page
  // filters on the presence of automationId for its automation chip.
  automationId?: string;
  triggerType?: string;
}

export function fromRunning(r: RunningRow): NormalisedSession {
  return {
    identifier: r.identifier,
    startedAt: r.startedAt,
    elapsedMs: r.elapsedMs,
    turnCount: r.turnCount,
    tokens: r.tokens,
    status: 'live',
    sessionId: r.sessionId,
    automationId: r.automationId,
    triggerType: r.triggerType,
  };
}

export function fromHistory(h: HistoryRow): NormalisedSession {
  return {
    identifier: h.identifier,
    title: h.title,
    startedAt: h.startedAt,
    finishedAt: h.finishedAt,
    elapsedMs: h.elapsedMs,
    turnCount: h.turnCount,
    tokens: h.tokens,
    status: h.status,
    sessionId: h.sessionId,
    automationId: h.automationId,
    triggerType: h.triggerType,
  };
}

// gaps_11 G-13(b) / todolist6 codex-B6 box 1 — dedup live/history sessions
// that share a sessionId, preferring the live row. The Timeline page passes
// `running` through `useStableValue` (held for up to 5s after a run exits),
// so a freshly-finished run can appear in BOTH sources within one render:
// stale 'live' row + fresh history row with the same sessionId. Sessions
// without a sessionId have no safe identity to dedup on and pass through.
export function dedupSessionsPreferLive(
  sessions: readonly NormalisedSession[],
): NormalisedSession[] {
  const indexBySession = new Map<string, number>();
  const out: NormalisedSession[] = [];
  for (const s of sessions) {
    if (!s.sessionId) {
      out.push(s);
      continue;
    }
    const at = indexBySession.get(s.sessionId);
    if (at === undefined) {
      indexBySession.set(s.sessionId, out.length);
      out.push(s);
    } else if (s.status === 'live' && out[at].status !== 'live') {
      // Replace in place so the duplicate keeps the first occurrence's
      // chronological position.
      out[at] = s;
    }
  }
  return out;
}

export interface IssueGroup {
  identifier: string;
  runs: NormalisedSession[];
  latestStatus: NormalisedSession['status'];
  latestStartedAt: string;
}

export interface SubagentSegment {
  name: string;
  startFrac: number;
  endFrac: number;
  logSlice: IssueLogEntry[];
}

export function extractSubagents(
  logs: IssueLogEntry[],
  filterSessionId?: string,
): SubagentSegment[] {
  const filtered = filterSessionId ? logs.filter((e) => e.sessionId === filterSessionId) : logs;
  if (filtered.length === 0) return [];
  const total = filtered.length;
  const markers = filtered.map((e, i) => ({ e, i })).filter(({ e }) => e.event === 'subagent');
  return markers.map(({ e, i }, si) => {
    const nextIdx = markers[si + 1]?.i ?? total;
    return {
      name: e.message.slice(0, 80),
      startFrac: i / total,
      endFrac: nextIdx / total,
      logSlice: filtered.slice(i, nextIdx),
    };
  });
}

// ─── Stable fallbacks ─────────────────────────────────────────────────────────

export { EMPTY_RUNNING, EMPTY_HISTORY } from '../../../utils/constants';

// ─── Utility: filter logs by run ──────────────────────────────────────────────

export function filterByRun(logs: IssueLogEntry[], run: NormalisedSession | null): IssueLogEntry[] {
  if (!run) return logs;
  const sid = run.sessionId;
  if (sid) {
    return logs.filter((e) => {
      if (e.sessionId) return e.sessionId === sid;
      if (!e.time) return false;
      const t = new Date(e.time).getTime();
      if (isNaN(t)) return false;
      const startMs = new Date(run.startedAt).getTime() - 5_000;
      const endMs = run.finishedAt
        ? new Date(run.finishedAt).getTime() + 5_000
        : Date.now() + 60_000;
      return t >= startMs && t <= endMs;
    });
  }
  const startMs = new Date(run.startedAt).getTime() - 5_000;
  const endMs = run.finishedAt ? new Date(run.finishedAt).getTime() + 5_000 : Date.now() + 60_000;
  return logs.filter((e) => {
    if (!e.time) return false;
    const t = new Date(e.time).getTime();
    if (isNaN(t)) return false;
    return t >= startMs && t <= endMs;
  });
}

// ─── Status dot style ─────────────────────────────────────────────────────────

export function dotStyle(status: NormalisedSession['status']): React.CSSProperties {
  switch (status) {
    case 'live':
      return {
        background: 'var(--success)',
        boxShadow: '0 0 0 4px rgba(34,197,94,0.2)',
      };
    case 'succeeded':
      return { background: 'var(--accent)' };
    case 'failed':
      return { background: 'var(--danger)' };
    case 'stalled':
      return { background: 'var(--warning, #f59e0b)' };
    case 'input_required':
      return { background: 'var(--warning, #f59e0b)', boxShadow: '0 0 0 4px rgba(245,158,11,0.2)' };
    default:
      return { background: 'var(--muted)' };
  }
}
