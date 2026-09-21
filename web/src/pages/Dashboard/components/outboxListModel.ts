// Pure helpers for OutboxList, kept out of the .tsx so they can be unit-tested
// directly and the component file only exports components.

/**
 * Reports whether `iso` parses to an instant strictly after `now` (epoch ms).
 * An absent or unparseable value is never "in the future": a rate-limited chip
 * must not render for a window that has already passed or cannot be read.
 *
 * `now` defaults to the wall clock, the same shape as liveOpsStripModel, so a
 * component can call it during render (react-hooks/purity forbids a bare
 * Date.now() in the component body) while tests pass an explicit instant.
 * OutboxList re-renders on every SSE snapshot, which re-evaluates it.
 */
export function isFutureInstant(iso: string | undefined, now: number = Date.now()): boolean {
  if (!iso) return false;
  const at = Date.parse(iso);
  return Number.isFinite(at) && at > now;
}
