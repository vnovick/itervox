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

export interface ConnectionState {
  /** True while an SSE channel is actively delivering events. */
  sseConnected: boolean;
  /** True once the first snapshot has landed. */
  hasSnapshot: boolean;
  /** True after CONNECTION_TIMEOUT_MS without either a snapshot or an open SSE channel. */
  timedOut: boolean;
  /** Convenience: snapshot is still missing AND the timeout has elapsed. */
  isOffline: boolean;
}

export function useConnectionState(): ConnectionState {
  const sseConnected = useItervoxStore((s) => s.sseConnected);
  const hasSnapshot = useItervoxStore((s) => s.snapshot !== null);
  const [timedOut, setTimedOut] = useState(false);

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

  return {
    sseConnected,
    hasSnapshot,
    timedOut,
    isOffline: !hasSnapshot && timedOut,
  };
}
