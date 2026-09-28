import { useEffect } from 'react';
import { useItervoxStore } from '../store/itervoxStore';
import { useNotifyPrefsStore } from '../store/notifyPrefsStore';
import { inputRequiredRowState } from '../utils/inputRequired';
import type { StateSnapshot } from '../types/schemas';

/**
 * CORE-096 — opt-in browser notifications when an issue starts waiting on
 * the operator (input_required) or ends in a final failure (enters `paused`
 * without being in `retrying` — retries are not final).
 *
 *  - Permission is requested only by `enable()`, which callers invoke from a
 *    click handler; the hook itself never prompts.
 *  - Unsupported (no-op, the title badge remains the signal) outside a secure
 *    context, without the Notification API, or when permission is 'denied'
 *    (never re-prompted).
 *  - Nothing fires while the page is visible; those transitions are marked
 *    seen, so they do not pop up later.
 *  - Dedupe: each transition has a stable key built from snapshot data — the
 *    input_required row's queuedAt, or the daemon session id plus the issue's
 *    latest history finishedAt for a pause — kept in localStorage, so SSE
 *    reconnects, reloads and other tabs of the same daemon session do not
 *    repeat it. The key is also the notification `tag`, which makes browsers
 *    replace (not stack) a duplicate raised by two tabs at the same instant.
 *  - The first snapshot after mount is a baseline: only changes notify.
 *
 * Mount with the default `watch: true` once (AttentionNotifier); settings UI
 * uses `watch: false` for the enable/disable controls only.
 */

const SEEN_KEY = 'itervox.notify.seen';
const SEEN_CAP = 200;

function notificationApi(): typeof Notification | undefined {
  return typeof Notification === 'undefined' ? undefined : Notification;
}

export function notificationsSupported(): boolean {
  const api = notificationApi();
  // Read loosely: some environments (older engines, jsdom) do not define it.
  const secure = (globalThis as { isSecureContext?: boolean }).isSecureContext === true;
  return secure && api !== undefined && api.permission !== 'denied';
}

// Fallback when localStorage is unavailable (private mode, blocked storage).
const memorySeen = new Set<string>();

/** Marks `key` seen; returns false when it already was. */
function claim(key: string): boolean {
  try {
    const raw = localStorage.getItem(SEEN_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    const seen = Array.isArray(parsed)
      ? parsed.filter((x): x is string => typeof x === 'string')
      : [];
    if (seen.includes(key)) return false;
    localStorage.setItem(SEEN_KEY, JSON.stringify([...seen, key].slice(-SEEN_CAP)));
    return true;
  } catch {
    if (memorySeen.has(key)) return false;
    memorySeen.add(key);
    return true;
  }
}

interface Transition {
  key: string;
  identifier: string;
  title: string;
  body: string;
}

/**
 * BH-M6-3 — pause reasons that mean "the run failed for good". Anything else —
 * an operator pause or cancel (`user_cancelled`), a dismissed question
 * (`user_dismissed_input`), an unknown value — or no reason at all (older
 * daemons) never notifies. These mirror the failure values of
 * internal/orchestrator/state.go PauseReason* exactly; stalls retry rather
 * than pause, so there is no stalled value.
 */
export const FAILURE_PAUSE_REASONS: ReadonlySet<string> = new Set([
  'retries_exhausted',
  'transition_failed',
]);

/**
 * The daemon's pause reason for `identifier`, if it sends one. Accepts the
 * `pauseReasons` map (sibling of `pausedWithPR`) and paused rows carrying
 * `pauseReason`; the snapshot schema entry is owned by the Go-side M6-close
 * fix, so it is read loosely here and absent means "unknown".
 */
export function pauseReasonOf(snapshot: StateSnapshot, identifier: string): string | undefined {
  const loose = snapshot as {
    pauseReasons?: Record<string, string | undefined>;
    pausedRows?: readonly { identifier: string; pauseReason?: string }[];
  };
  return (
    loose.pauseReasons?.[identifier] ??
    loose.pausedRows?.find((r) => r.identifier === identifier)?.pauseReason
  );
}

function inputRows(s: StateSnapshot) {
  return (s.inputRequired ?? []).filter((r) => inputRequiredRowState(r) === 'input_required');
}

export function attentionTransitions(prev: StateSnapshot, next: StateSnapshot): Transition[] {
  const out: Transition[] = [];
  const before = new Set(inputRows(prev).map((r) => `${r.identifier}\u0000${r.queuedAt}`));
  for (const row of inputRows(next)) {
    if (before.has(`${row.identifier}\u0000${row.queuedAt}`)) continue;
    out.push({
      key: `input:${row.identifier}:${row.queuedAt}`,
      identifier: row.identifier,
      title: `${row.identifier} needs your input`,
      body: row.context.slice(0, 180),
    });
  }
  const wasPaused = new Set(prev.paused);
  const retrying = new Set(next.retrying.map((r) => r.identifier));
  for (const id of next.paused) {
    if (wasPaused.has(id) || retrying.has(id)) continue;
    const reason = pauseReasonOf(next, id);
    if (reason === undefined || !FAILURE_PAUSE_REASONS.has(reason)) continue;
    const finished = (next.history ?? [])
      .filter((h) => h.identifier === id)
      .map((h) => h.finishedAt)
      .sort()
      .pop();
    out.push({
      key: `paused:${next.currentAppSessionId ?? ''}:${id}:${finished ?? ''}`,
      identifier: id,
      title: `${id} stopped and is paused`,
      body: 'The agent run failed and will not be retried. Open the dashboard to resume or discard it.',
    });
  }
  return out;
}

export function useAttentionNotifications({ watch = true }: { watch?: boolean } = {}) {
  const optIn = useNotifyPrefsStore((s) => s.optIn);
  const setOptIn = useNotifyPrefsStore((s) => s.setOptIn);
  const supported = notificationsSupported();
  const enabled = supported && optIn && notificationApi()?.permission === 'granted';

  useEffect(() => {
    if (!watch || !enabled) return;
    return useItervoxStore.subscribe((state, prevState) => {
      const next = state.snapshot;
      const prev = prevState.snapshot;
      if (!next || !prev || next === prev) return;
      const api = notificationApi();
      if (!api) return;
      for (const t of attentionTransitions(prev, next)) {
        if (!claim(t.key)) continue;
        if (document.visibilityState === 'visible') continue;
        const n = new api(t.title, { body: t.body, tag: t.key });
        n.onclick = () => {
          window.focus();
          useItervoxStore.getState().setSelectedIdentifier(t.identifier);
        };
      }
    });
  }, [watch, enabled]);

  return {
    supported,
    enabled,
    /** Call from a click handler only: it may show the browser's permission prompt. */
    enable: async (): Promise<boolean> => {
      const api = notificationApi();
      if (!api || !notificationsSupported()) return false;
      const permission = api.permission === 'granted' ? 'granted' : await api.requestPermission();
      setOptIn(permission === 'granted');
      return permission === 'granted';
    },
    disable: () => {
      setOptIn(false);
    },
  };
}
