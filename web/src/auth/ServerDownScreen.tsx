import { useEffect, useRef } from 'react';
import { useAuthStore } from './authStore';
import { SERVER_DOWN_RETRY_MS } from '../utils/timings';
import { StatusCard, StatusScreen } from '../components/common/StatusCard';
import { plainButtonClass } from '../components/ui/button/buttonRecipe';

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
    <StatusScreen>
      <StatusCard eyebrow="Daemon unreachable" title="Can't reach the daemon">
        <p>
          <code className="text-theme-text font-mono text-[13px] font-semibold">
            /api/v1/health
          </code>{' '}
          is unreachable. Check that <code className="font-mono text-[13px]">itervox</code> is
          running and the host/port match your dashboard URL.
        </p>
        <p>Retrying automatically every {SERVER_DOWN_RETRY_MS / 1000} seconds.</p>
        <button
          type="button"
          className={plainButtonClass('primary', 'lg')}
          onClick={() => {
            setStatus('unknown');
          }}
        >
          Retry
        </button>
      </StatusCard>
    </StatusScreen>
  );
}
