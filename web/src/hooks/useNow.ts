import { useEffect, useState } from 'react';

/**
 * CORE-090 — a clock that advances on its own, so relative times ("active 8s
 * ago") move between snapshot pushes. Mount it once per list (one interval
 * for the whole table) and pass `now` down; never one per row.
 */
export function useNow(intervalMs = 1_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => {
      setNow(Date.now());
    }, intervalMs);
    return () => {
      clearInterval(id);
    };
  }, [intervalMs]);
  return now;
}
