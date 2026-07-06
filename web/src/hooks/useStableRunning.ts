import { useEffect, useRef, useState, startTransition } from 'react';
import { LOG_STABLE_DELAY_MS } from '../utils/timings';
import type { RunningRow } from '../types/schemas';

/**
 * Holds the most recent non-empty `running` snapshot for a short stable
 * window after the snapshot empties out. Without this, transient zero-row
 * snapshots between SSE bursts cause the running-sessions table to flash
 * empty briefly before the next snapshot fills it again.
 *
 * Returns `running` directly when it's non-empty; otherwise returns the
 * cached stable copy until LOG_STABLE_DELAY_MS elapses, then commits to
 * the empty list.
 *
 * Extracted from RunningSessionsTable.tsx (T-58) so reuse across other
 * row-flicker-prone tables is straightforward.
 */
export function useStableRunning(running: RunningRow[]): RunningRow[] {
  const [stable, setStable] = useState<RunningRow[]>(running);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (running.length > 0) {
      startTransition(() => {
        setStable(running);
      });
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
    } else if (timerRef.current === null) {
      timerRef.current = setTimeout(() => {
        setStable([]);
        timerRef.current = null;
      }, LOG_STABLE_DELAY_MS);
    }
    return () => {
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
    };
  }, [running]);

  return running.length > 0 ? running : stable;
}
