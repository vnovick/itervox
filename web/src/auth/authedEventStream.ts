import { fetchEventSource, type EventSourceMessage } from '@microsoft/fetch-event-source';
import { getToken, useTokenStore } from './tokenStore';
import { useAuthStore } from './authStore';
import { SSE_RECONNECT_BASE_MS, SSE_RECONNECT_MAX_MS } from '../utils/timings';

/**
 * Replaces native EventSource with a fetch-based SSE client that CAN send
 * `Authorization: Bearer <token>` headers (native EventSource cannot).
 *
 * Uses `@microsoft/fetch-event-source` under the hood. The library handles
 * reconnection when `onerror` returns a number (delay in ms); we throw from
 * `onerror` to signal a fatal error (auth failure) that should stop retries.
 */

// Sentinel: thrown from onopen/onerror to abort without triggering reconnect.
class FatalSSEError extends Error {
  constructor(msg: string) {
    super(msg);
    this.name = 'FatalSSEError';
  }
}

// Sentinel: thrown from onclose to force @microsoft/fetch-event-source to
// retry. The library's `create()` loop only reconnects when the request
// promise rejects (see lib/esm/fetch.js: a clean stream end calls
// `onclose(); dispose(); resolve();` — returning normally from onclose
// resolves the outer promise for good, so a proxy/LB graceful close would
// otherwise end the stream forever). Throwing here routes through the
// library's catch block into our onerror, which applies backoff and
// notifies onDisconnect — so onclose itself must NOT also call
// onDisconnect, or every clean close would notify twice.
class RetryableCloseError extends Error {
  constructor() {
    super('sse closed cleanly by server; reconnecting');
    this.name = 'RetryableCloseError';
  }
}

export interface AuthedEventStreamOptions {
  /** Called for each SSE message (default channel — no `event:` field). */
  onMessage: (msg: EventSourceMessage) => void;
  /** Called once the stream connects (server responded 200). */
  onOpen?: () => void;
  /** Called when the stream disconnects (transient — a reconnect will follow). */
  onDisconnect?: () => void;
}

/**
 * Opens an authenticated SSE stream. Returns a closer function.
 *
 * On 401 at open time: clears the token, flips auth status, stops retrying.
 * On other failures: reconnects with exponential backoff (1s → 15s cap).
 */
export function openAuthedEventStream(url: string, opts: AuthedEventStreamOptions): () => void {
  const ctrl = new AbortController();
  let attempt = 0;

  void fetchEventSource(url, {
    signal: ctrl.signal,
    // Keep streaming when the tab is hidden (matches native EventSource).
    openWhenHidden: true,
    headers: ((): Record<string, string> => {
      const token = getToken();
      const h: Record<string, string> = {};
      if (token) h.Authorization = `Bearer ${token}`;
      return h;
    })(),
    onopen(res) {
      // fetchEventSource accepts either a sync or Promise-returning onopen.
      // We don't need to await anything here, so a sync function avoids the
      // @typescript-eslint/require-await lint warning.
      if (res.status === 401) {
        useTokenStore.getState().clearToken();
        queueMicrotask(() => {
          useAuthStore.getState().markUnauthorized();
        });
        throw new FatalSSEError('unauthorized');
      }
      if (!res.ok) {
        throw new Error(`sse open failed: ${String(res.status)}`);
      }
      attempt = 0;
      opts.onOpen?.();
      return Promise.resolve();
    },
    onmessage(msg) {
      opts.onMessage(msg);
    },
    onclose() {
      if (ctrl.signal.aborted) {
        // Deliberate close (unmount/navigation) — don't notify or retry.
        return;
      }
      // Clean server-side close: throw so the library's catch routes into
      // onerror (below), which applies backoff and notifies onDisconnect.
      // Do NOT call opts.onDisconnect?.() here too, or a clean close would
      // notify twice.
      throw new RetryableCloseError();
    },
    onerror(err) {
      if (err instanceof FatalSSEError) {
        // Rethrow → library stops retrying.
        throw err;
      }
      opts.onDisconnect?.();
      // Exponential backoff with a cap. Returning a number tells the library
      // how long to wait before the next attempt.
      const delay = Math.min(SSE_RECONNECT_BASE_MS * 2 ** attempt, SSE_RECONNECT_MAX_MS);
      attempt += 1;
      return delay;
    },
  }).catch(() => {
    // Swallow: onerror/onopen already handled user-visible state.
  });

  return () => {
    ctrl.abort();
  };
}
