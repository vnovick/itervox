// CORE-076 — the single status model. AppHeader, LiveOpsStrip, HeroStats and
// the operator queue (lib/operatorQueue.ts) all count and label orchestrator
// states through this module, so one snapshot can never produce two
// different numbers or two words for the same thing.
//
// - `needsInput` counts input_required rows only; pending_input_resume rows
//   are `resuming` (the reply is in, nothing for the operator to do).
// - Row state is read through inputRequiredRowState, so rows from daemons
//   that omit `state` keep the context-prefix fallback.
// - "Live" is the SSE connection only (`live`). Whether agents are running
//   is `activity`: 'active' | 'waiting' | 'offline' (no snapshot yet).
// - The attention total needs the tracker issue list (review items), so it
//   lives with the queue: buildOperatorQueueItems(...).attention.

import type { StateSnapshot } from '../types/schemas';
import { inputRequiredRowState } from '../utils/inputRequired';

export type StatusKey =
  | 'running'
  | 'paused'
  | 'retrying'
  | 'input_required'
  | 'pending_input_resume'
  | 'idle';

export type StatusTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral';

export interface StatusMeta {
  /** Title-case label for tiles, chips and group headings. */
  label: string;
  /** Lower-case form for "N <noun>" pills. */
  noun: string;
  tone: StatusTone;
}

export const STATUS_META: Record<StatusKey, StatusMeta> = {
  running: { label: 'Running', noun: 'running', tone: 'success' },
  paused: { label: 'Paused', noun: 'paused', tone: 'warning' },
  retrying: { label: 'Retrying', noun: 'retrying', tone: 'warning' },
  input_required: { label: 'Needs input', noun: 'needs input', tone: 'warning' },
  pending_input_resume: { label: 'Resuming', noun: 'resuming', tone: 'info' },
  idle: { label: 'Idle', noun: 'idle', tone: 'neutral' },
};

export type StatusActivity = 'active' | 'waiting' | 'offline';

export const ACTIVITY_LABEL: Record<StatusActivity, string> = {
  active: 'Active',
  waiting: 'Waiting',
  offline: 'Offline',
};

/** The snapshot branches summarizeStatus reads (LiveOpsStrip selects exactly these). */
export type StatusInput = Pick<
  StateSnapshot,
  'running' | 'paused' | 'retrying' | 'inputRequired' | 'maxConcurrentAgents'
>;

export interface StatusSummary {
  hasSnapshot: boolean;
  running: number;
  paused: number;
  retrying: number;
  /** input_required rows only. */
  needsInput: number;
  /** pending_input_resume rows (read-only). */
  resuming: number;
  maxAgents: number;
  /** running / maxAgents as a rounded percentage; 0 without a cap. */
  capacityPct: number;
  /** SSE connection state — the only meaning of "Live". */
  live: boolean;
  activity: StatusActivity;
  /** The single state the header shows, by fixed precedence. */
  headline: StatusKey;
}

export function countInputRows(rows: StateSnapshot['inputRequired']): {
  needsInput: number;
  resuming: number;
} {
  let needsInput = 0;
  let resuming = 0;
  for (const row of rows ?? []) {
    if (inputRequiredRowState(row) === 'pending_input_resume') resuming++;
    else needsInput++;
  }
  return { needsInput, resuming };
}

export function summarizeStatus(
  snapshot: StatusInput | null | undefined,
  { sseConnected }: { sseConnected: boolean },
): StatusSummary {
  if (!snapshot) {
    return {
      hasSnapshot: false,
      running: 0,
      paused: 0,
      retrying: 0,
      needsInput: 0,
      resuming: 0,
      maxAgents: 0,
      capacityPct: 0,
      live: sseConnected,
      activity: 'offline',
      headline: 'idle',
    };
  }
  const running = snapshot.running.length;
  const paused = snapshot.paused.length;
  const retrying = snapshot.retrying.length;
  const { needsInput, resuming } = countInputRows(snapshot.inputRequired);
  const maxAgents = snapshot.maxConcurrentAgents;
  // Precedence: running, then a reply still needed (M5-close: it used to sit
  // below Resuming, so the chip said "Resuming" while an operator reply was
  // outstanding), then resuming, retrying, paused.
  const headline: StatusKey =
    running > 0
      ? 'running'
      : needsInput > 0
        ? 'input_required'
        : resuming > 0
          ? 'pending_input_resume'
          : retrying > 0
            ? 'retrying'
            : paused > 0
              ? 'paused'
              : 'idle';
  return {
    hasSnapshot: true,
    running,
    paused,
    retrying,
    needsInput,
    resuming,
    maxAgents,
    capacityPct: maxAgents > 0 ? Math.round((running / maxAgents) * 100) : 0,
    live: sseConnected,
    activity: running > 0 ? 'active' : 'waiting',
    headline,
  };
}

/** CSS colour token per tone, for surfaces that colour a number. */
export function toneColor(tone: StatusTone): string | undefined {
  switch (tone) {
    case 'success':
      return 'var(--success)';
    case 'warning':
      return 'var(--warning)';
    case 'danger':
      return 'var(--danger)';
    case 'info':
      return 'var(--accent)';
    default:
      return undefined;
  }
}
