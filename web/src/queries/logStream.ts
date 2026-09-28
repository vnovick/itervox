import type { IssueLogEntry } from '../types/schemas';

/**
 * Client-side state of one live log stream (CORE-027).
 *
 * The server resumes a reconnecting stream after the `Last-Event-ID` that
 * `@microsoft/fetch-event-source` resends, so the lines already on screen are
 * NOT replayed — the panel must keep them. Frame ids are `<generation>-<seq>`
 * on both streams: for the issue-log stream the generation is the daemon's
 * logbuffer epoch; for the sublog stream it is the session-file set's epoch
 * (CORE-153 — replaced or cleared session files start a new epoch, announced
 * by a `gap` frame `<epoch>-0` followed by a full replay). A bare `<seq>`
 * (older daemons' sublog stream) still parses, as generation ''. The rules:
 *
 * - a line whose seq is at or below the last one shown (same generation) is a
 *   replay and is dropped, so nothing is duplicated;
 * - a line from a different generation (daemon restart or config reload: a
 *   new numbering) replaces the list instead of being de-duplicated against
 *   numbers that no longer mean the same thing;
 * - a `gap` frame (id = the seq just before the replay that follows) is shown
 *   as an explicit marker: after the kept history when the replay starts past
 *   it ("N log lines were skipped"), or as a reset of the list when the replay
 *   would overlap what is shown or the generation changed;
 * - a new stream instance (identifier change, or the panel re-opening the
 *   stream) starts over: its first connection replays the full window.
 */
export interface LogStreamState {
  identifier: string;
  /** Identifies the stream instance (one per effect run) the state belongs to. */
  instance: number;
  generation: string | null;
  lastSeq: number | null;
  entries: IssueLogEntry[];
}

export const LIVE_LOG_ENTRY_CAP = 500;

export function emptyLogStream(identifier: string, instance = 0): LogStreamState {
  return { identifier, instance, generation: null, lastSeq: null, entries: [] };
}

export function appendCappedLogEntry(prev: IssueLogEntry[], entry: IssueLogEntry): IssueLogEntry[] {
  if (prev.length >= LIVE_LOG_ENTRY_CAP) {
    return [...prev.slice(prev.length - LIVE_LOG_ENTRY_CAP + 1), entry];
  }
  return [...prev, entry];
}

/** Parses `<generation>-<seq>` or `<seq>`; null when the id carries no seq. */
export function parseStreamId(id: string | undefined): { generation: string; seq: number } | null {
  if (!id) return null;
  const dash = id.lastIndexOf('-');
  const generation = dash >= 0 ? id.slice(0, dash) : '';
  const seqText = dash >= 0 ? id.slice(dash + 1) : id;
  if (!/^\d+$/.test(seqText)) return null;
  return { generation, seq: Number(seqText) };
}

function marker(message: string): IssueLogEntry {
  return { level: 'WARN', event: 'warn', message };
}

const RESET_MESSAGE =
  'Log history was reset on the server (daemon restart, config reload or cleared log). Showing the lines it still retains.';

function skippedMessage(count: number | null): string {
  const what = count === null ? 'Some log lines were' : `${String(count)} log lines were`;
  return `${what} skipped: they left the server's retained window before this view caught up.`;
}

function forStream(prev: LogStreamState, identifier: string, instance: number): LogStreamState {
  return prev.identifier === identifier && prev.instance === instance
    ? prev
    : emptyLogStream(identifier, instance);
}

/** Applies one streamed log line. */
export function applyLogEntry(
  prev: LogStreamState,
  identifier: string,
  instance: number,
  id: string | undefined,
  entry: IssueLogEntry,
): LogStreamState {
  let s = forStream(prev, identifier, instance);
  const pid = parseStreamId(id);
  if (!pid) return { ...s, entries: appendCappedLogEntry(s.entries, entry) };
  if (s.generation !== null && s.generation !== pid.generation) {
    s = { ...emptyLogStream(identifier, instance), entries: [marker(RESET_MESSAGE)] };
  } else if (s.lastSeq !== null && pid.seq <= s.lastSeq) {
    return s; // replayed after a reconnect: already shown
  }
  return {
    ...s,
    generation: pid.generation,
    lastSeq: pid.seq,
    entries: appendCappedLogEntry(s.entries, entry),
  };
}

/** Applies a server `gap` frame; see LogStreamState. */
export function applyLogGap(
  prev: LogStreamState,
  identifier: string,
  instance: number,
  id: string | undefined,
): LogStreamState {
  const s = forStream(prev, identifier, instance);
  const pid = parseStreamId(id);
  if (!pid) {
    return {
      ...s,
      lastSeq: null,
      entries: appendCappedLogEntry(s.entries, marker(skippedMessage(null))),
    };
  }
  if (s.entries.length === 0) {
    // Nothing shown yet, so nothing was skipped from this view's perspective.
    return { ...s, generation: pid.generation, lastSeq: pid.seq };
  }
  const generationChanged = s.generation !== null && s.generation !== pid.generation;
  const overlaps = s.lastSeq !== null && pid.seq < s.lastSeq;
  if (generationChanged || overlaps) {
    return {
      ...s,
      generation: pid.generation,
      lastSeq: pid.seq,
      entries: [marker(RESET_MESSAGE)],
    };
  }
  // The server only sends a gap for a real discontinuity, so it is always
  // shown; the count is given when the numbering says how many were skipped.
  const skipped = s.lastSeq !== null && pid.seq > s.lastSeq ? pid.seq - s.lastSeq : null;
  return {
    ...s,
    generation: pid.generation,
    lastSeq: pid.seq,
    entries: appendCappedLogEntry(s.entries, marker(skippedMessage(skipped))),
  };
}
