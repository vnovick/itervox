// CORE-095 — the host wires Mod+K and the g-sequences to the palette/router.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router';
import { QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { makeTestQueryClient } from '../../../test/queryClient';
import { CommandPaletteHost } from '../CommandPaletteHost';
import { useUIStore } from '../../../store/uiStore';
import { PAGES } from '../commandPalettePages';

vi.mock('../../../queries/issues', () => ({
  useIssues: () => ({ data: [] }),
  useTerminateIssue: () => ({ mutate: vi.fn(), isPending: false }),
  useCancelIssue: () => ({ mutate: vi.fn(), isPending: false }),
}));

// The palette's server-backed sources are not under test here.
vi.mock('../palette/usePaletteSources', () => ({ usePaletteSources: () => [] }));

// The palette chunk loads on first open; under a busy machine that can take
// longer than findBy's 1 s default.
const LAZY = { timeout: 5_000 };

function Providers({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={makeTestQueryClient()}>{children}</QueryClientProvider>;
}

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>;
}

beforeEach(() => {
  useUIStore.setState({ commandPaletteOpen: false });
});

describe('CommandPaletteHost', () => {
  it('Mod+K opens the palette; each g-sequence navigates', async () => {
    render(
      <Providers>
        <MemoryRouter>
          <CommandPaletteHost />
          <Where />
        </MemoryRouter>
      </Providers>,
    );
    fireEvent.keyDown(document.body, { key: 'k', ctrlKey: true });
    expect(useUIStore.getState().commandPaletteOpen).toBe(true);
    // The palette UI is lazy-loaded on first open (M6-close bundle fix).
    expect(
      await screen.findByRole('dialog', { name: 'Command palette' }, LAZY),
    ).toBeInTheDocument();
    act(() => {
      useUIStore.getState().setCommandPaletteOpen(false);
    });
    for (const page of PAGES) {
      const [prefix, key] = page.keys.split(' ');
      fireEvent.keyDown(document.body, { key: prefix });
      fireEvent.keyDown(document.body, { key });
      expect(screen.getByTestId('where')).toHaveTextContent(page.path);
    }
  });

  it('closing from the palette clears the store flag', async () => {
    render(
      <Providers>
        <MemoryRouter>
          <CommandPaletteHost />
        </MemoryRouter>
      </Providers>,
    );
    act(() => {
      useUIStore.getState().setCommandPaletteOpen(true);
    });
    await screen.findByRole('dialog', { name: 'Command palette' }, LAZY);
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(useUIStore.getState().commandPaletteOpen).toBe(false);
  });
});
