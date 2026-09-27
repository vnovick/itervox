// CORE-087 — an unknown route renders inside AppShell (sidebar nav + header),
// and the page keeps its Back to Home link.
import { describe, expect, it, vi } from 'vitest';
import { QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { AppRouteTable } from '../App';
import { makeTestQueryClient } from '../test/queryClient';
import { ThemeProvider } from '../context/ThemeContext';

vi.mock('../auth/authedFetch', () => ({
  authedFetch: vi.fn(() => Promise.resolve(new Response('[]', { status: 200 }))),
}));

describe('App', () => {
  it('404 renders inside AppShell nav', async () => {
    render(
      <QueryClientProvider client={makeTestQueryClient()}>
        <MemoryRouter initialEntries={['/definitely-not-a-page']}>
          <ThemeProvider>
            <AppRouteTable />
          </ThemeProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByRole('link', { name: 'Back to Home Page' })).toHaveAttribute(
      'href',
      '/',
    );
    // The shell's sidebar navigation is present around the 404 page.
    expect(screen.getAllByRole('link', { name: /^Logs/ }).length).toBeGreaterThan(0);
    expect(screen.getByTestId('header-orchestrator-state')).toBeInTheDocument();
  });
});
