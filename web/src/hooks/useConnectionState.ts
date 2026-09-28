import { startTransition, useEffect, useState } from 'react';
import { useItervoxStore } from '../store/itervoxStore';

/**
 * useConnectionState centralises the offline-detection pattern that both the
 * Dashboard's "Connecting…" overlay and the AppHeader's live/reconnecting
 * label need to consume. Both surfaces used to maintain independent
 * setTimeout loops (Dashboard at 8s, AppHeader at 6s), which could legitimately
 * disagree on whether the daemon was offline — showing "Connected" in one
 * place while the other rendered "Disconnected". v0.2.0 audit P2-10.
 *
 * Single source of truth at 8s (the more conservative of the two prior
 * timeouts) so any surface consuming this hook agrees on the verdict.
 */
const CONNECTION_TIMEOUT_MS = 8000;

// CORE-023 — a snapshot is only considered stale once BOTH signals are old:
// no SSE message (including keepalives) in the last 60s — or none ever, when
// SSE never opened — AND no successfully
// parsed snapshot (SSE or poll) in the last 60s either. Either signal being
// fresh means the daemon is reachable, so 60s matches the ~25-30s server
// keepalive cadence with margin, and deliberately does NOT key off a poll
// interval (which would flag an idle-but-healthy daemon as stale, since
// snapshots are only pushed on change).
const STALE_THRESHOLD_MS = 60_000;
// How often to re-evaluate staleness while mounted. The store fields only
// change on new messages/snapshots, so without a periodic re-render the
// hook would never notice time passing while the channel is silent.
const STALE_CHECK_INTERVAL_MS = 5_000;

export interface ConnectionState {
  /** True while an SSE channel is actively delivering events. */
  sseConnected: boolean;
  /** True once the first snapshot has landed. */
  hasSnapshot: boolean;
  /** True after CONNECTION_TIMEOUT_MS without either a snapshot or an open SSE channel. */
  timedOut: boolean;
  /** Convenience: snapshot is still missing AND the timeout has elapsed. */
  isOffline: boolean;
  /**
   * True once a snapshot exists but both the SSE channel (lastMessageAt,
   * keepalives included) and the polling fallback (lastSnapshotAt) have
   * gone quiet for longer than STALE_THRESHOLD_MS. A keepalive or a
   * successful poll each independently clear this.
   */
  isStale: boolean;
  /** ms epoch of the last successfully parsed snapshot, or null. */
  lastSnapshotAt: number | null;
  /** ms since the last successfully parsed snapshot, or null. Precomputed
   * here (rather than left to the caller to do `Date.now() - lastSnapshotAt`
   * at render time) so consuming components stay pure. */
  dataAgeMs: number | null;
}

export function useConnectionState(): ConnectionState {
  const sseConnected = useItervoxStore((s) => s.sseConnected);
  const hasSnapshot = useItervoxStore((s) => s.snapshot !== null);
  const lastMessageAt = useItervoxStore((s) => s.lastMessageAt);
  const lastSnapshotAt = useItervoxStore((s) => s.lastSnapshotAt);
  const [timedOut, setTimedOut] = useState(false);
  // `now` is only ever written from inside an effect/interval callback
  // (never read via a live Date.now() call during render), so this stays a
  // pure computation of already-known state.
  const [now, setNow] = useState(0);

  useEffect(() => {
    if (hasSnapshot || sseConnected) {
      startTransition(() => {
        setTimedOut(false);
      });
      return;
    }
    const t = setTimeout(() => {
      setTimedOut(true);
    }, CONNECTION_TIMEOUT_MS);
    return () => {
      clearTimeout(t);
    };
  }, [hasSnapshot, sseConnected]);

  // Re-render periodically so a stream that's gone silent (no new
  // lastMessageAt/lastSnapshotAt writes) still crosses the stale threshold
  // on schedule, instead of only re-evaluating on the next store update.
  // `Date.now()` is only ever called here, inside the effect/interval — not
  // during render — to keep the hook's render output pure.
  useEffect(() => {
    const tick = () => {
      setNow(Date.now());
    };
    tick();
    const id = setInterval(tick, STALE_CHECK_INTERVAL_MS);
    return () => {
      clearInterval(id);
    };
  }, []);

  const isOld = (ts: number): boolean => now - ts > STALE_THRESHOLD_MS;
  // SSE side: a null lastMessageAt means the channel has never delivered a
  // single frame (never opened, or blocked by a proxy). That is silence, not
  // "unknown" — treating it as fresh would let a polling-only dashboard never
  // go stale (M0-close G5). A number is judged by age as usual.
  const sseSilent = lastMessageAt === null || isOld(lastMessageAt);
  // Snapshot side: judged by the age of the last parsed snapshot. A snapshot
  // present without a recorded time gives no age to judge, so it cannot on
  // its own raise the banner.
  const snapshotSilent = typeof lastSnapshotAt === 'number' && isOld(lastSnapshotAt);
  const isStale = hasSnapshot && sseSilent && snapshotSilent;
  const dataAgeMs = typeof lastSnapshotAt === 'number' ? Math.max(0, now - lastSnapshotAt) : null;

  return {
    sseConnected,
    hasSnapshot,
    timedOut,
    isOffline: !hasSnapshot && timedOut,
    isStale,
    lastSnapshotAt,
    dataAgeMs,
  };
}
