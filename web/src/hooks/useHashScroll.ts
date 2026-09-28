import { useEffect, useRef } from 'react';
import { useLocation } from 'react-router';
import { usePrefersReducedMotion } from './usePrefersReducedMotion';

/**
 * CORE-086 — React Router v7 does not scroll to hash targets. The dashboard
 * mounts this once: when the location hash names an element (the sections a
 * header pill or ops chip links to), that element scrolls into view.
 *
 * `ready` is any value that changes when section content may have appeared
 * (e.g. row counts), so a link followed before the first snapshot still lands
 * once the section renders. Each navigation scrolls at most once — later
 * snapshot pushes never pull the page back while the operator reads.
 *
 * CORE-024 — scrollIntoView's `behavior` is explicit, so the CSS
 * `scroll-behavior: auto !important` under `prefers-reduced-motion: reduce`
 * cannot override it; the preference is honoured here.
 */
export function useHashScroll(ready: unknown): void {
  const { hash, key } = useLocation();
  const prefersReducedMotion = usePrefersReducedMotion();
  const handledKey = useRef<string | null>(null);
  useEffect(() => {
    if (!hash || handledKey.current === key) return;
    const target = document.getElementById(decodeURIComponent(hash.slice(1)));
    if (!target) return;
    handledKey.current = key;
    target.scrollIntoView({ behavior: prefersReducedMotion ? 'auto' : 'smooth', block: 'start' });
  }, [hash, key, ready, prefersReducedMotion]);
}
