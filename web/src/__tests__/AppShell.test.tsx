import { describe, expect, it, vi } from 'vitest';
import { QueryClientProvider } from '@tanstack/react-query';
import { makeTestQueryClient } from '../test/queryClient';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { AppShell } from '../App';
import { ThemeProvider } from '../context/ThemeContext';

// CORE-078 — the sidebar reads the attention count (snapshot + issues
// query), so the shell now needs a QueryClient; the issues fetch is stubbed.
vi.mock('../auth/authedFetch', () => ({
  authedFetch: vi.fn(() => Promise.resolve(new Response('[]', { status: 200 }))),
}));

function renderAppShell() {
  return render(
    <QueryClientProvider client={makeTestQueryClient()}>
      <MemoryRouter>
        <ThemeProvider>
          <AppShell />
        </ThemeProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// CORE-024 — the closed mobile nav drawer stayed in the tab order and the
// a11y tree below the md breakpoint (role="dialog" aria-modal, hidden only
// via opacity-0/pointer-events-none). `inert` now removes it from both
// while `mobileNavOpen` is false.
describe('AppShell', () => {
  it('closed drawer is inert', () => {
    renderAppShell();
    const drawer = screen.getByRole('dialog', { name: 'Navigation' });
    expect(drawer).toHaveAttribute('inert');
  });

  it('open drawer is not inert (useFocusTrap keeps working)', async () => {
    renderAppShell();
    const user = userEvent.setup();
    await user.click(screen.getByTestId('mobile-menu-button'));
    const drawer = screen.getByRole('dialog', { name: 'Navigation' });
    expect(drawer).not.toHaveAttribute('inert');
  });

  // M5-close BH-M5-6 — widening the window past md hid the drawer (md:hidden)
  // but left it open, so its focus trap and inert-free state persisted.
  it('closes the drawer when the viewport crosses the md breakpoint', async () => {
    const listeners = new Set<(e: { matches: boolean }) => void>();
    const mql = {
      matches: false,
      media: '(min-width: 768px)',
      addEventListener: (_: string, fn: (e: { matches: boolean }) => void) => listeners.add(fn),
      removeEventListener: (_: string, fn: (e: { matches: boolean }) => void) =>
        listeners.delete(fn),
    };
    const original = window.matchMedia;
    window.matchMedia = vi.fn(() => mql) as unknown as typeof window.matchMedia;
    try {
      renderAppShell();
      const user = userEvent.setup();
      await user.click(screen.getByTestId('mobile-menu-button'));
      const drawer = screen.getByRole('dialog', { name: 'Navigation' });
      expect(drawer).not.toHaveAttribute('inert');
      expect(listeners.size).toBeGreaterThan(0);
      act(() => {
        mql.matches = true;
        for (const fn of listeners) fn({ matches: true });
      });
      expect(drawer).toHaveAttribute('inert');
    } finally {
      window.matchMedia = original;
    }
  });
});
