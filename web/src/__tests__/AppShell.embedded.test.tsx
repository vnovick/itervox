// Embedded mode (the desktop app's window): no sidebar or drawer, a top bar
// with `project · ISSUE`, Board / Sessions / New task / More, the More menu
// reaches every page, and the first load of "/" lands on the issue view.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { QueryClientProvider } from '@tanstack/react-query';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { AppRouteTable } from '../App';
import { makeTestQueryClient } from '../test/queryClient';
import { ThemeProvider } from '../context/ThemeContext';
import { resetEmbeddedForTests } from '../hooks/useEmbeddedShell';
import { useItervoxStore } from '../store/itervoxStore';
import { makeRunningRow, makeSnapshot } from '../test/fixtures/snapshots';
import { EMBEDDED_PAGES } from '../layout/embeddedPages';
import { resetAllStores } from '../test/resetStores';

vi.mock('../auth/authedFetch', () => ({
  authedFetch: vi.fn(() => Promise.resolve(new Response('[]', { status: 200 }))),
}));

function Where() {
  const loc = useLocation();
  return <output data-testid="where">{loc.pathname + loc.search}</output>;
}

function renderApp(route = '/') {
  return render(
    <QueryClientProvider client={makeTestQueryClient()}>
      <MemoryRouter initialEntries={[route]}>
        <ThemeProvider>
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

describe('embedded shell', () => {
  it('has no sidebar or drawer; the top bar names the project and the focused issue', async () => {
    resetEmbeddedForTests(true);
    renderApp('/');
    // First load of "/" lands on the issue view, which picked the running issue.
    expect(await screen.findByRole('heading', { name: 'Issue view' })).toBeInTheDocument();
    expect(screen.getByTestId('where')).toHaveTextContent(/^\/focus$/);
    expect(screen.getByTestId('embedded-title')).toHaveTextContent('web-app · WEB-207');
    expect(screen.queryByRole('dialog', { name: 'Navigation' })).toBeNull();
    expect(screen.queryByTestId('mobile-menu-button')).toBeNull();
    expect(screen.queryByRole('link', { name: 'Dashboard' })).toBeNull();
    const nav = screen.getByRole('navigation', { name: 'Pages' });
    expect(within(nav).getByRole('link', { name: 'Board' })).toHaveAttribute('href', '/');
    expect(within(nav).getByRole('link', { name: 'Sessions' })).toHaveAttribute(
      'href',
      '/sessions',
    );
    expect(screen.getByRole('button', { name: /Commands/ })).toBeInTheDocument();
  });

  it('Board goes to the board and stays there', async () => {
    resetEmbeddedForTests(true);
    const user = userEvent.setup();
    renderApp('/');
    await screen.findByRole('heading', { name: 'Issue view' });
    await user.click(
      within(screen.getByRole('navigation', { name: 'Pages' })).getByRole('link', {
        name: 'Board',
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

  it('the More menu lists every page and follows the keyboard', async () => {
    resetEmbeddedForTests(true);
    const user = userEvent.setup();
    renderApp('/settings');
    const more = await screen.findByRole('button', { name: 'More' });
    expect(more).toHaveAttribute('aria-expanded', 'false');
    await user.click(more);
    const menu = screen.getByRole('menu', { name: 'All pages' });
    const items = within(menu).getAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual(EMBEDDED_PAGES.map((p) => p.label));
    expect(items.map((i) => i.getAttribute('href'))).toEqual(EMBEDDED_PAGES.map((p) => p.path));
    expect(within(menu).getByRole('menuitem', { name: 'Settings' })).toHaveAttribute(
      'aria-current',
      'page',
    );
    // Focus starts on the first item; arrows move, Escape returns to More.
    expect(items[0]).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(items[1]).toHaveFocus();
    await user.keyboard('{ArrowUp}{ArrowUp}');
    expect(items[items.length - 1]).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(more).toHaveFocus();
    // Following an item navigates and closes the menu.
    await user.click(more);
    await user.click(screen.getByRole('menuitem', { name: 'Repo Analysis' }));
    expect(screen.getByTestId('where')).toHaveTextContent('/repo');
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('not embedded: the sidebar stays and "/" is the board', async () => {
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
    expect(screen.queryByRole('button', { name: 'More' })).toBeNull();
    expect(screen.queryByTestId('embedded-title')).toBeNull();
  });
});
