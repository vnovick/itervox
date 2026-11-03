// ?setting=<key> on /settings (the command palette's settings rows): once the
// schema and the layer's form are loaded, open the field's section, then
// focus its control and scroll it into view. Each navigation is handled
// once, so later edits never pull the page back.

import { useEffect, useRef } from 'react';
import { useLocation, useSearchParams } from 'react-router';
import type { SettingsFieldDef } from '../../../lib/settingsSchema';

/** How many animation frames to wait for the opened section to render. */
const MAX_FRAMES = 30;

function fieldBox(key: string): HTMLElement | null {
  for (const el of document.querySelectorAll<HTMLElement>('[data-testid^="setting-"]')) {
    if (el.dataset.testid === `setting-${key}`) return el;
  }
  return null;
}

export function useSettingJump(
  fields: readonly SettingsFieldDef[],
  ready: boolean,
  reveal: (section: string) => void,
): void {
  const [params] = useSearchParams();
  const key = params.get('setting');
  // A navigation is handled once: the same link followed again (a new
  // history entry) lands again.
  const visit = `${useLocation().key}:${key ?? ''}`;
  const handled = useRef<string | null>(null);
  const raf = useRef(0);
  const def = key ? fields.find((f) => f.key === key) : undefined;

  // Cancel a pending landing only on unmount: revealing the section
  // re-renders this component, and that must not cancel it.
  useEffect(
    () => () => {
      cancelAnimationFrame(raf.current);
    },
    [],
  );

  useEffect(() => {
    if (!key || !def || !ready || handled.current === visit) return;
    handled.current = visit;
    reveal(def.section);
    let frames = 0;
    const land = () => {
      const box = fieldBox(key);
      // Wait until the field is rendered and its section has opened (a
      // control inside a closed <details> cannot take focus).
      if (!box || box.closest('details')?.open === false) {
        if (++frames < MAX_FRAMES) raf.current = requestAnimationFrame(land);
        return;
      }
      const control = box.querySelector<HTMLElement>('input, select, textarea');
      control?.focus({ preventScroll: true });
      // Not every environment implements scrollIntoView (jsdom).
      const target: Partial<Pick<Element, 'scrollIntoView'>> = box;
      target.scrollIntoView?.({ block: 'center' });
    };
    raf.current = requestAnimationFrame(land);
  }, [key, visit, def, ready, reveal]);
}
