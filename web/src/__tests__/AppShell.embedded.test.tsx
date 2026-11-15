// Embedded mode (the desktop app's window, under its project tabs): the
// same sidebar as the web (plus Issue view), no top bar page menu, a top bar
// titled `project · ISSUE`, the Go menu's navigate hook, and the first load
// of "/" lands on the issue view.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { QueryClientProvider } from '@tanstack/react-query';
import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { AppRouteTable } from '../App';
import { useEmbeddedNavigateHook } from '../hooks/useEmbeddedShell';
import { makeTestQueryClient } from '../test/queryClient';
import { ThemeProvider } from '../context/ThemeContext';
import { resetEmbeddedForTests } from '../hooks/useEmbeddedShell';
import { useItervoxStore } from '../store/itervoxStore';
import { makeRunningRow, makeSnapshot } from '../test/fixtures/snapshots';
import { resetAllStores } from '../test/resetStores';

vi.mock('../auth/authedFetch', () => ({
  authedFetch: vi.fn(() => Promise.resolve(new Response('[]', { status: 200 }))),
}));

function Where() {
  const loc = useLocation();
  return <output data-testid="where">{loc.pathname + loc.search}</output>;
}

function NavigateHook() {
  useEmbeddedNavigateHook();
  return null;
}

function renderApp(route = '/') {
  return render(
    <QueryClientProvider client={makeTestQueryClient()}>
      <MemoryRouter initialEntries={[route]}>
        <ThemeProvider>
          <NavigateHook />
          <AppRouteTable />
          <Where />
        </ThemeProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  resetAllStores();
  useItervoxStore.setState({
    snapshot: makeSnapshot({
      projectName: 'web-app',
      running: [makeRunningRow({ identifier: 'WEB-207' })],
    }),
  });
});

afterEach(() => {
  resetEmbeddedForTests();
});

// The sidebar's labels, top to bottom (the desktop sidebar and the drawer
// render the same list; the drawer is inert while closed).
function sidebarLabels(): string[] {
  const aside = document.querySelector<HTMLElement>('aside');
  if (!aside) return [];
  return within(aside)
    .getAllByRole('link')
    .map((l) => l.textContent.replace(/\d+$/, ''));
}

const WEB_PAGES = [
  'Dashboard',
  'Timeline',
  'Sessions',
  'Logs',
  'Agents',
  'Automations',
  'Rules',
  'Repo Analysis',
  'Memory',
  'Settings',
];

describe('embedded shell', () => {
  it('shows the web sidebar (plus Issue view) and no top bar page menu', async () => {
    resetEmbeddedForTests(true);
    renderApp('/');
    // First load of "/" lands on the issue view, which picked the running issue.
    expect(await screen.findByRole('heading', { name: 'Issue view' })).toBeInTheDocument();
    expect(screen.getByTestId('where')).toHaveTextContent(/^\/focus$/);
    expect(screen.getByTestId('embedded-title')).toHaveTextContent('web-app · WEB-207');
    expect(document.querySelector('[data-shell="embedded"]')).not.toBeNull();
    expect(sidebarLabels()).toEqual(['Dashboard', 'Issue view', ...WEB_PAGES.slice(1)]);
    expect(
      within(document.querySelector('aside') as HTMLElement).getByRole('link', {
        name: 'Issue view',
      }),
    ).toHaveAttribute('aria-current', 'page');
    // The phone drawer is the web one too.
    expect(screen.getByRole('dialog', { name: 'Navigation', hidden: true })).toBeInTheDocument();
    expect(screen.getByTestId('mobile-menu-button')).toBeInTheDocument();
    // No top bar page navigation or More menu any more.
    expect(screen.queryByRole('navigation', { name: 'Pages' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'More' })).toBeNull();
    expect(screen.queryByRole('menu', { name: 'All pages' })).toBeNull();
    expect(screen.getByRole('button', { name: /Commands/ })).toBeInTheDocument();
  });

  it('the sidebar goes to the board and stays there', async () => {
    resetEmbeddedForTests(true);
    const user = userEvent.setup();
    renderApp('/');
    await screen.findByRole('heading', { name: 'Issue view' });
    await user.click(
      within(document.querySelector('aside') as HTMLElement).getByRole('link', {
        name: /^Dashboard/,
      }),
    );
    expect(screen.getByTestId('where')).toHaveTextContent(/^\/$/);
    expect(
      await screen.findByRole(
        'heading',
        { name: 'Autonomous agentic harness' },
        { timeout: 10_000 },
      ),
    ).toBeVisible();
  });

  it("the desktop app's Go menu navigates through window.__itervoxNavigate", async () => {
    resetEmbeddedForTests(true);
    renderApp('/settings');
    await screen.findByTestId('where');
    expect(typeof window.__itervoxNavigate).toBe('function');
    let ok: boolean | undefined;
    act(() => {
      ok = window.__itervoxNavigate?.('/rules');
    });
    expect(ok).toBe(true);
    expect(screen.getByTestId('where')).toHaveTextContent(/^\/rules$/);
    act(() => {
      ok = window.__itervoxNavigate?.('//evil.example/');
    });
    expect(ok).toBe(false);
    expect(screen.getByTestId('where')).toHaveTextContent(/^\/rules$/);
  });

  it('a deep link keeps the board', async () => {
    resetEmbeddedForTests(true);
    renderApp('/?view=list');
    expect(
      await screen.findByRole(
        'heading',
        { name: 'Autonomous agentic harness' },
        { timeout: 10_000 },
      ),
    ).toBeVisible();
    expect(screen.getByTestId('where')).toHaveTextContent('/?view=list');
  });

  it('not embedded: the sidebar has no Issue view, "/" is the board, no navigate hook', async () => {
    resetEmbeddedForTests(false);
    renderApp('/');
    expect(
      await screen.findByRole(
        'heading',
        { name: 'Autonomous agentic harness' },
        { timeout: 10_000 },
      ),
    ).toBeVisible();
    expect(screen.getByRole('dialog', { name: 'Navigation', hidden: true })).toBeInTheDocument();
    expect(sidebarLabels()).toEqual(WEB_PAGES);
    expect(screen.queryByTestId('embedded-title')).toBeNull();
    expect(document.querySelector('[data-shell="embedded"]')).toBeNull();
    expect(window.__itervoxNavigate).toBeUndefined();
  });
});
