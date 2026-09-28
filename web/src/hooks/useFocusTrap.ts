import { useEffect, useRef, type RefObject } from 'react';

const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'textarea:not([disabled])',
  'select:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(', ');

interface FocusTrapOptions {
  /**
   * Whether the trap is currently enforcing Tab containment. Defaults to
   * true. Stacked dialogs pass `false` for every layer but the top one
   * (CORE-067), so a Modal over a SlidePanel owns Tab while the panel keeps
   * its saved opener for the eventual restore.
   */
  active?: boolean;
  /**
   * The element to restore focus to on close, captured by the caller before
   * the dialog's children mounted (useDialogLayer does this during render).
   * Needed when content uses `autoFocus`: React moves focus into the dialog
   * during commit, before this hook's effect runs, so `document.activeElement`
   * is already the dialog's own input by then (M5-close). When omitted, the
   * active element at open time is used.
   */
  opener?: HTMLElement | null;
}

/**
 * Traps keyboard focus within a container while `isOpen` is true.
 *
 * On open:  saves `document.activeElement`.
 * While open and active: focuses the first focusable child unless focus is
 *   already inside; Tab at the last element wraps to the first, Shift+Tab at
 *   the first wraps to the last, and Tab from anywhere outside the container
 *   is pulled back in.
 * On close: restores focus to the previously focused element.
 */
export function useFocusTrap(
  containerRef: RefObject<HTMLElement | null>,
  isOpen: boolean,
  { active = true, opener }: FocusTrapOptions = {},
): void {
  const previouslyFocusedRef = useRef<HTMLElement | null>(null);
  const openerRef = useRef(opener);
  useEffect(() => {
    openerRef.current = opener;
  }, [opener]);

  // Save / restore the opener — tied to open state only, so losing the top
  // of the overlay stack does not bounce focus back to the opener.
  useEffect(() => {
    if (!isOpen) return;
    previouslyFocusedRef.current =
      openerRef.current !== undefined
        ? openerRef.current
        : (document.activeElement as HTMLElement | null);
    return () => {
      const prev = previouslyFocusedRef.current;
      previouslyFocusedRef.current = null;
      if (prev && typeof prev.focus === 'function' && prev.isConnected) {
        prev.focus();
      }
    };
  }, [isOpen]);

  const enforcing = isOpen && active;

  useEffect(() => {
    if (!enforcing) return;
    const container = containerRef.current;
    if (!container) return;

    // Small delay so the DOM has rendered focusable children.
    const rafId = requestAnimationFrame(() => {
      if (container.contains(document.activeElement)) return;
      const focusable = container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR);
      if (focusable.length > 0) {
        focusable[0].focus();
      } else {
        // If no focusable child exists, make the container itself focusable.
        container.setAttribute('tabindex', '-1');
        container.focus();
      }
    });

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key !== 'Tab') return;
      if (!container) return;

      const focusable = container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR);
      if (focusable.length === 0) return;

      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const current = document.activeElement;

      if (!container.contains(current)) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
        return;
      }
      if (event.shiftKey) {
        if (current === first) {
          event.preventDefault();
          last.focus();
        }
      } else if (current === last) {
        event.preventDefault();
        first.focus();
      }
    }

    document.addEventListener('keydown', handleKeyDown);

    return () => {
      cancelAnimationFrame(rafId);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [enforcing, containerRef]);
}
