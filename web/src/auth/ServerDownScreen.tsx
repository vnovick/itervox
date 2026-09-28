import { useEffect, useRef } from 'react';
import { useAuthStore } from './authStore';
import { SERVER_DOWN_RETRY_MS } from '../utils/timings';

/**
 * Shown when /api/v1/health fails with a network error — distinguishes
 * "daemon not running" from "auth misconfigured".
 *
 * CORE-089 — re-probes on its own every SERVER_DOWN_RETRY_MS through
 * `onRetry` (AuthGate's probe, which ignores a tick while a probe is still in
 * flight). The manual Retry only flips the auth status back to 'unknown':
 * AuthGate's effect then probes exactly once and unmounts this screen, which
 * clears the timer. It used to call onRetry as well, probing twice per click.
 * This file makes no request itself.
 */
export function ServerDownScreen({ onRetry }: { onRetry: () => void }) {
  const setStatus = useAuthStore((s) => s.setStatus);
  // The latest onRetry, so a new callback identity does not restart the timer.
  const onRetryRef = useRef(onRetry);
  useEffect(() => {
    onRetryRef.current = onRetry;
  }, [onRetry]);
  useEffect(() => {
    const id = setInterval(() => {
      onRetryRef.current();
    }, SERVER_DOWN_RETRY_MS);
    return () => {
      clearInterval(id);
    };
  }, []);
  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <div className="bg-theme-bg-soft border-theme-line max-w-md rounded-[var(--radius-lg)] border p-6 text-center shadow-xl">
        <h1 className="mb-2 text-xl font-semibold">Can't reach the daemon</h1>
        <p className="text-theme-muted mb-4 text-sm">
          <code className="font-mono text-xs">/api/v1/health</code> is unreachable. Check that{' '}
          <code className="font-mono text-xs">itervox</code> is running and the host/port match your
          dashboard URL.
        </p>
        <p className="text-theme-muted mb-4 text-xs">
          Retrying automatically every {SERVER_DOWN_RETRY_MS / 1000} seconds.
        </p>
        <button
          type="button"
          onClick={() => {
            setStatus('unknown');
          }}
          className="rounded-[var(--radius-md)] bg-[color:var(--color-accent)] px-4 py-2 text-sm font-medium text-white"
        >
          Retry
        </button>
      </div>
    </div>
  );
}
