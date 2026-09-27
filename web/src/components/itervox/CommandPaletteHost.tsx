import { Suspense, lazy, useMemo } from 'react';
import { useNavigate } from 'react-router';
import { useUIStore } from '../../store/uiStore';
import { useHotkeys, type Hotkey } from '../../hooks/useHotkeys';
// M6-close — the palette UI (Modal, primitives, tailwind-merge) loads on
// first open; only the hotkeys stay in the main entry.
const CommandPalette = lazy(async () => {
  const mod = await import('./CommandPalette');
  return { default: mod.CommandPalette };
});
import { PAGES } from './commandPalettePages';

/**
 * CORE-095 — owns the global shortcuts (Mod+K opens the palette; g d / g l /
 * g t / g a / g u / g s navigate) and renders the palette. Mounted once,
 * inside the router.
 */
export function CommandPaletteHost() {
  const navigate = useNavigate();
  const open = useUIStore((s) => s.commandPaletteOpen);
  const setOpen = useUIStore((s) => s.setCommandPaletteOpen);
  const hotkeys = useMemo<Hotkey[]>(
    () => [
      {
        keys: 'mod+k',
        run: () => {
          setOpen(true);
        },
      },
      ...PAGES.map((p) => ({
        keys: p.keys,
        run: () => {
          void navigate(p.path);
        },
      })),
    ],
    [navigate, setOpen],
  );
  useHotkeys(hotkeys);
  if (!open) return null;
  return (
    <Suspense fallback={null}>
      <CommandPalette
        isOpen
        onClose={() => {
          setOpen(false);
        }}
      />
    </Suspense>
  );
}
