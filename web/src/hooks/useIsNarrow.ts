import { useEffect, useState } from 'react';

// Phone width: below Tailwind's `sm` breakpoint.
const QUERY = '(max-width: 639px)';

function read(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false;
  return window.matchMedia(QUERY).matches;
}

/**
 * Whether the viewport is phone width (< 640px), tracked live. For layout
 * decisions CSS cannot make on its own, such as rendering only the unified
 * diff on a phone. False where matchMedia is unavailable.
 */
export function useIsNarrow(): boolean {
  const [narrow, setNarrow] = useState(read);
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const mql = window.matchMedia(QUERY);
    const onChange = (event: MediaQueryListEvent) => {
      setNarrow(event.matches);
    };
    if (typeof mql.addEventListener !== 'function') return;
    mql.addEventListener('change', onChange);
    return () => {
      mql.removeEventListener('change', onChange);
    };
  }, []);
  return narrow;
}
