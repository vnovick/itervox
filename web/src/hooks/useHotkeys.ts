import { useEffect, useRef } from 'react';
import { topLayerId } from '../components/ui/dialog/overlayStack';

/**
 * CORE-095 — global keyboard shortcuts.
 *
 * `keys` is either `mod+<key>` (Ctrl on Windows/Linux, Cmd on macOS) or a
 * two-key sequence `"<prefix> <key>"` such as `g l`. Rules:
 *  - ignored while composing (IME), while focus is in an input, textarea,
 *    select or contenteditable, while any dialog / SlidePanel is open (they
 *    own Escape and focus via the overlay stack), and during a dnd-kit
 *    keyboard drag (its handle has aria-pressed=true; arrows/space/enter/
 *    escape belong to the drag);
 *  - a sequence prefix expires after SEQUENCE_TIMEOUT_MS;
 *  - preventDefault only when an action actually runs, so unbound keys and
 *    the browser's own shortcuts are left alone.
 */
export interface Hotkey {
  keys: string;
  run: () => void;
}

export const SEQUENCE_TIMEOUT_MS = 1_000;

function isEditable(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable || target.closest('[contenteditable=""], [contenteditable="true"]'))
    return true;
  const tag = target.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}

function dragActive(): boolean {
  return document.querySelector('[aria-roledescription="draggable"][aria-pressed="true"]') !== null;
}

export function useHotkeys(hotkeys: readonly Hotkey[]): void {
  const hotkeysRef = useRef(hotkeys);
  useEffect(() => {
    hotkeysRef.current = hotkeys;
  }, [hotkeys]);

  useEffect(() => {
    let prefix: { key: string; at: number } | null = null;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.isComposing || event.defaultPrevented) return;
      if (isEditable(event.target) || topLayerId() !== null || dragActive()) {
        prefix = null;
        return;
      }
      const key = event.key.toLowerCase();
      const list = hotkeysRef.current;
      if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey) {
        const hit = list.find((h) => h.keys === `mod+${key}`);
        if (hit) {
          event.preventDefault();
          hit.run();
        }
        prefix = null;
        return;
      }
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (prefix && Date.now() - prefix.at <= SEQUENCE_TIMEOUT_MS) {
        const hit = list.find((h) => h.keys === `${prefix?.key ?? ''} ${key}`);
        prefix = null;
        if (hit) {
          event.preventDefault();
          hit.run();
        }
        return;
      }
      prefix = list.some((h) => h.keys.startsWith(`${key} `)) ? { key, at: Date.now() } : null;
    };
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
    };
  }, []);
}
