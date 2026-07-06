import { useEffect } from 'react';
import { useItervoxStore } from '../store/itervoxStore';
import { StateSnapshotSchema } from '../types/schemas';
import type { StateSnapshot } from '../types/schemas';
import { authedFetch } from '../auth/authedFetch';
import { openAuthedEventStream } from '../auth/authedEventStream';
import { UnauthorizedError } from '../auth/UnauthorizedError';

// SSE_SILENCE_THRESHOLD_MS — if no SSE message arrives within this window while
// the connection is "open", treat the stream as buffered/blocked (corporate
// proxy with infinite SSE buffering, dead Cloudflare path, etc.) and fall back
// to polling /api/v1/state. The proxy-buffer scenario is the one motivating
// case from the manual checklist (T-27): the connection looks healthy from the
// browser's POV (no `onclose`/`onerror`), but no bytes flow.
//
// 30s matches the server-side keepalive cadence — anything longer than 30s
// of silence is anomalous and warrants a fallback poll.
const SSE_SILENCE_THRESHOLD_MS = 30_000;

// SSE_SILENCE_CHECK_INTERVAL_MS — how often the watchdog checks whether the
// silence threshold has been exceeded. 5s gives a worst-case detection
// latency of (threshold + interval) ≈ 35s before the first fallback poll fires.
const SSE_SILENCE_CHECK_INTERVAL_MS = 5_000;

/**
 * Connects to /api/v1/events (SSE) and keeps the Zustand snapshot up to date.
 * Uses @microsoft/fetch-event-source under the hood so the connection can
 * carry `Authorization: Bearer <token>` headers.
 *
 * Falls back to polling /api/v1/state every 3s while SSE is down. Also runs a
 * silence watchdog: if no SSE message arrives within SSE_SILENCE_THRESHOLD_MS
 * (30s) while the connection is open, polling kicks in even though
 * `onDisconnect` never fired (corporate proxy buffering scenario, T-27).
 * Polling stops automatically when SSE resumes.
 *
 * Mounts once.
 */
export function useItervoxSSE() {
  useEffect(() => {
    const { setSnapshot, setSseConnected } = useItervoxStore.getState();

    let pollTimer: ReturnType<typeof setInterval> | null = null;
    let silenceTimer: ReturnType<typeof setInterval> | null = null;
    let sseWorking = false;
    let cancelled = false;
    let lastEventAt = 0; // ms epoch of last SSE message; 0 until first.

    async function poll() {
      if (sseWorking || cancelled) return;
      try {
        const res = await authedFetch('/api/v1/state');
        if (res.ok) {
          const snap = StateSnapshotSchema.parse(await res.json());
          setSnapshot(snap);
        }
      } catch (err) {
        if (err instanceof UnauthorizedError) return; // AuthGate will handle.
      }
    }

    function startPoll() {
      if (pollTimer) return;
      void poll();
      pollTimer = setInterval(() => {
        void poll();
      }, 3000);
    }

    function stopPoll() {
      if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
      }
    }

    function startSilenceWatchdog() {
      if (silenceTimer) return;
      silenceTimer = setInterval(() => {
        if (cancelled) return;
        // Only enforce silence-fallback while SSE thinks it's working — once
        // a real disconnect fires, the regular polling path is already active.
        if (!sseWorking) return;
        if (lastEventAt === 0) return; // not yet received first event; don't penalize handshake
        const silentMs = Date.now() - lastEventAt;
        if (silentMs >= SSE_SILENCE_THRESHOLD_MS) {
          // Treat the stream as silently broken: surface as not-connected to
          // the UI and start polling. We do NOT close the SSE connection here
          // — fetch-event-source will continue trying; if a message arrives
          // we'll see it via onMessage and the poll stops.
          if (import.meta.env.DEV) {
            console.warn(
              `[itervox] SSE silent for ${String(silentMs)}ms — falling back to polling`,
            );
          }
          sseWorking = false;
          setSseConnected(false);
          startPoll();
        }
      }, SSE_SILENCE_CHECK_INTERVAL_MS);
    }

    function stopSilenceWatchdog() {
      if (silenceTimer) {
        clearInterval(silenceTimer);
        silenceTimer = null;
      }
    }

    const close = openAuthedEventStream('/api/v1/events', {
      onOpen: () => {
        sseWorking = true;
        setSseConnected(true);
        lastEventAt = Date.now(); // reset so handshake counts as "fresh"
        stopPoll();
        startSilenceWatchdog();
      },
      onMessage: (msg) => {
        if (!msg.data) return;
        // ANY incoming SSE message — keepalive or snapshot — proves the
        // connection is alive, so bump the silence watchdog timestamp
        // unconditionally. Server-side keepalive is `event: keepalive` (see
        // handlers.go::handleEvents); skip the snapshot parse for those so
        // we don't log a parse warning every 25s on a quiet system.
        lastEventAt = Date.now();
        if (!sseWorking) {
          sseWorking = true;
          setSseConnected(true);
          stopPoll();
        }
        if (msg.event === 'keepalive') return;
        try {
          const snap: StateSnapshot = StateSnapshotSchema.parse(JSON.parse(msg.data));
          setSnapshot(snap);
        } catch (err) {
          if (import.meta.env.DEV) {
            console.warn('[itervox] SSE message parse/validation failed', err);
          }
        }
      },
      onDisconnect: () => {
        sseWorking = false;
        setSseConnected(false);
        stopSilenceWatchdog();
        startPoll();
      },
    });

    // Start polling immediately so the dashboard shows data during SSE handshake.
    startPoll();

    return () => {
      cancelled = true;
      sseWorking = false;
      stopPoll();
      stopSilenceWatchdog();
      close();
      setSseConnected(false);
    };
  }, []);
}
