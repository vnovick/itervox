import { useEffect } from 'react';
import { useItervoxStore } from '../store/itervoxStore';
import { openAuthedEventStream } from '../auth/authedEventStream';

/**
 * Streams log lines from /api/v1/logs into the Zustand store.
 * Accepts an optional identifier to filter logs server-side.
 * Uses the authed SSE helper so it sends the bearer token.
 *
 * CORE-075 — mounted by NarrativeFeed (the only reader of `s.logs`), not at
 * the app root, so other routes hold no global log connection. Lines are
 * buffered and appended once per animation frame: a burst of N lines costs
 * one store notification, not N. Lines still buffered on unmount are flushed
 * in order before the stream closes.
 *
 * The server replays the last 16 KiB of the log on every connect (it sends no
 * ids and does not resume from Last-Event-ID), so each (re)connection
 * REPLACES the buffer rather than appending to it. That also covers a
 * Dashboard remount: the new stream's open clears what the previous mount
 * left before the replayed tail arrives. Streams are closed and reconnected
 * on every daemon config reload (CORE-025); appending made each reload
 * re-append the tail already on screen.
 */
export function useLogStream(identifier?: string) {
  useEffect(() => {
    const { appendLogs, clearLogs } = useItervoxStore.getState();

    const url = identifier
      ? `/api/v1/logs?identifier=${encodeURIComponent(identifier)}`
      : '/api/v1/logs';

    let pending: string[] = [];
    let frame: number | null = null;

    const flush = () => {
      frame = null;
      if (pending.length === 0) return;
      const batch = pending;
      pending = [];
      try {
        appendLogs(batch);
      } catch (err) {
        if (import.meta.env.DEV) {
          console.warn('[itervox] useLogStream: appendLogs threw', err);
        }
      }
    };

    const close = openAuthedEventStream(url, {
      // Fires before the replayed tail arrives on this connection.
      onOpen: () => {
        pending = [];
        if (frame !== null) {
          cancelAnimationFrame(frame);
          frame = null;
        }
        clearLogs();
      },
      onMessage: (msg) => {
        if (msg.event !== 'log') return;
        pending.push(msg.data);
        frame ??= requestAnimationFrame(flush);
      },
    });

    return () => {
      if (frame !== null) cancelAnimationFrame(frame);
      flush();
      close();
    };
  }, [identifier]);
}
