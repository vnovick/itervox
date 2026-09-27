// CORE-095 — the host wires Mod+K and the g-sequences to the palette/router.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router';
import { CommandPaletteHost } from '../CommandPaletteHost';
import { useUIStore } from '../../../store/uiStore';
import { PAGES } from '../commandPalettePages';

vi.mock('../../../queries/issues', () => ({
  useIssues: () => ({ data: [] }),
  useTerminateIssue: () => ({ mutate: vi.fn(), isPending: false }),
  useCancelIssue: () => ({ mutate: vi.fn(), isPending: false }),
}));

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>;
}

beforeEach(() => {
  useUIStore.setState({ commandPaletteOpen: false });
});

describe('CommandPaletteHost', () => {
  it('Mod+K opens the palette; each g-sequence navigates', async () => {
    render(
      <MemoryRouter>
        <CommandPaletteHost />
        <Where />
      </MemoryRouter>,
    );
    fireEvent.keyDown(document.body, { key: 'k', ctrlKey: true });
    expect(useUIStore.getState().commandPaletteOpen).toBe(true);
    // The palette UI is lazy-loaded on first open (M6-close bundle fix).
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeInTheDocument();
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
      <MemoryRouter>
        <CommandPaletteHost />
      </MemoryRouter>,
    );
    act(() => {
      useUIStore.getState().setCommandPaletteOpen(true);
    });
    await screen.findByRole('dialog', { name: 'Command palette' });
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(useUIStore.getState().commandPaletteOpen).toBe(false);
  });
});
