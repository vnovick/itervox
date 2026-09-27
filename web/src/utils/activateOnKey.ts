import type { KeyboardEvent } from 'react';

/**
 * CORE-088 — keyboard activation for a `role="button"` element that cannot be
 * a real <button> (its content includes block elements): Enter and Space run
 * the handler, as they would on a native button.
 */
export function activateOnKey(handler: () => void) {
  return (event: KeyboardEvent<HTMLElement>) => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      handler();
    }
  };
}
