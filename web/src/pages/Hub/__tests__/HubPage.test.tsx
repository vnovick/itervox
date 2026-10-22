import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import HubPage from '../index';
import { configureApiContext } from '../../../auth/apiBase';

const PROJECTS = {
  hub: { version: 'v0.2.9', pid: 42, url: 'http://127.0.0.1:8089/hub/', startedAt: '', headSeq: 7 },
  projects: [
    {
      name: 'alpha',
      workflow: '/w/alpha/WORKFLOW.md',
      projectDir: '/w/alpha',
      pid: 1,
      url: 'http://127.0.0.1:8090/',
      version: 'v0.2.9',
      startedAt: '',
      health: 'ok',
      proxied: true,
      capabilities: ['dispatch_pause'],
      summary: { counts: { running: 2, inputRequired: 1 }, paused: false },
    },
    {
      name: 'legacy',
      workflow: '/w/legacy/WORKFLOW.md',
      projectDir: '/w/legacy',
      pid: 2,
      url: 'http://127.0.0.1:8091/',
      version: 'v0.2.0',
      startedAt: '',
      health: 'error',
      proxied: false,
      proxyError: 'runs with server.allow_unauthenticated',
      capabilities: null,
    },
  ],
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <HubPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  configureApiContext('/hub/');
});
afterEach(() => {
  configureApiContext('/');
  vi.restoreAllMocks();
});

// #228 — the hub's project switcher.
describe('HubPage', () => {
  it('lists every member with its health, counts and a link to its dashboard', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify(PROJECTS), { status: 200 }));
    global.fetch = fetchMock as unknown as typeof fetch;
    renderPage();
    const list = await screen.findByRole('list', { name: 'Projects' });
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/hub/api/v1/projects');
    const [first, second, ...rest] = await within(list).findAllByRole('listitem');
    expect(rest).toHaveLength(0);
    expect(within(first).getByText('alpha')).toBeTruthy();
    expect(within(first).getByText(/2 running · 1 need input/)).toBeTruthy();
    expect(within(first).getByRole('link', { name: 'Open dashboard' }).getAttribute('href')).toBe(
      '/hub/p/alpha/',
    );
    expect(within(second).queryByRole('link')).toBeNull();
    expect(within(second).getByTestId('hub-not-proxied').textContent).toContain(
      'allow_unauthenticated',
    );
    expect(screen.getByText(/2 projects · itervox v0.2.9/)).toBeTruthy();
  });

  it('says so when there are no members', async () => {
    global.fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ ...PROJECTS, projects: [] }), { status: 200 }),
      ) as unknown as typeof fetch;
    renderPage();
    expect(await screen.findByText('No projects yet')).toBeTruthy();
  });
});
