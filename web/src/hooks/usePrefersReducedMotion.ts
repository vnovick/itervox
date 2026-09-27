import { useEffect, useState } from 'react';

const QUERY = '(prefers-reduced-motion: reduce)';

function readPreference(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false;
  return window.matchMedia(QUERY).matches;
}

/**
 * CORE-024 — tracks the `prefers-reduced-motion: reduce` media query.
 *
 * `src/index.css` already carries a global `@media (prefers-reduced-motion:
 * reduce)` block that zeroes transition/animation durations for every real
 * browser via `!important` — that's the right mechanism for anything driven
 * by CSS transitions/animations, and nothing here should duplicate it.
 *
 * This hook exists for the cases CSS genuinely cannot reach: imperative
 * scrolling APIs like `Element.scrollIntoView({ behavior: 'smooth' })` pass
 * `behavior` as an explicit call argument, so the CSS `scroll-behavior: auto
 * !important` rule in that same block does NOT override it. Call sites that
 * animate a scroll (`PendingResumePanel.tsx`, `AgentLogPanel.tsx`) use this
 * hook to pass `'auto'` instead of `'smooth'` when the user prefers reduced
 * motion. Do not reach for this hook to duplicate something the CSS block
 * already covers (e.g. transition/animation durations) — that was tried and
 * reverted (CORE-024 fix round 1) because it added inline styles a
 * non-`!important` value can never win against, purely so a jsdom test
 * could assert something the CSS already guarantees in production.
 */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(readPreference);

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const mql = window.matchMedia(QUERY);
    const handleChange = (event: MediaQueryListEvent) => {
      setReduced(event.matches);
    };
    if (typeof mql.addEventListener === 'function') {
      mql.addEventListener('change', handleChange);
      return () => {
        mql.removeEventListener('change', handleChange);
      };
    }
    return undefined;
  }, []);

  return reduced;
}
