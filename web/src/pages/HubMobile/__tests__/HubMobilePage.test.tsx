import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import HubMobilePage from '../index';
import { configureApiContext, configureApiOrigin } from '../../../auth/apiBase';
import { projectsNeedingAttention } from '../../../queries/hubPhone';
import { HubProjectsResponseSchema } from '../../../types/schemas';

const project = (name: string, counts: Record<string, number>, proxied = true) => ({
  name,
  workflow: `/w/${name}/WORKFLOW.md`,
  projectDir: `/w/${name}`,
  pid: 1,
  url: 'http://127.0.0.1:8090/',
  version: 'v0.2.9',
  startedAt: '',
  health: 'ok',
  proxied,
  capabilities: [],
  summary: { counts, paused: false },
});

const PROJECTS = {
  hub: { version: 'v0.2.9', pid: 42, url: 'http://127.0.0.1:8089/hub/', startedAt: '', headSeq: 7 },
  projects: [
    project('alpha', { running: 1, inputRequired: 1, recentFailures: 1 }),
    project('beta', { running: 2 }),
    project('legacy', { inputRequired: 3 }, false),
  ],
};

const ALPHA_STATE = {
  inputRequired: [
    {
      identifier: 'ENG-1',
      sessionId: 's1',
      state: 'input_required',
      context: 'Which database should I migrate?',
      queuedAt: '2026-10-22T10:00:00Z',
    },
    {
      identifier: 'ENG-2',
      sessionId: 's2',
      state: 'pending_input_resume',
      context: 'already answered',
      queuedAt: '2026-10-22T09:00:00Z',
    },
  ],
  recentFailures: [
    {
      kind: 'worker_failed',
      identifier: 'ENG-3',
      message: 'exit status 1',
      occurredAt: '2026-10-22T11:00:00Z',
      recordedAt: '2026-10-22T11:00:00Z',
      count: 1,
    },
    {
      kind: 'tracker_poll',
      message: 'rate limited',
      occurredAt: '2026-10-22T11:00:00Z',
      recordedAt: '2026-10-22T11:00:00Z',
      count: 1,
    },
  ],
  running: [],
};

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <HubMobilePage />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  configureApiContext('/hub/m');
});
afterEach(() => {
  configureApiContext('/');
  configureApiOrigin(null);
  vi.restoreAllMocks();
});

// #231 — the phone's needs-attention list across every project.
describe('HubMobilePage needs-attention list', () => {
  it('lists waiting questions and failed runs across projects, linking to the mobile issue page', async () => {
    const fetchMock = vi.fn((url: string) =>
      Promise.resolve(
        new Response(JSON.stringify(url.endsWith('/state') ? ALPHA_STATE : PROJECTS), {
          status: 200,
        }),
      ),
    );
    global.fetch = fetchMock as unknown as typeof fetch;
    renderPage();

    const section = await screen.findByRole('region', { name: 'Needs attention' });
    const links = await within(section).findAllByRole('link');
    expect(links).toHaveLength(2);
    expect(links[0]?.getAttribute('href')).toBe('/hub/p/alpha/m/ENG-1');
    expect(links[0]?.textContent).toContain('Which database should I migrate?');
    expect(links[0]?.textContent).toContain('Needs you');
    expect(links[1]?.getAttribute('href')).toBe('/hub/p/alpha/m/ENG-3');
    expect(links[1]?.textContent).toContain('Failed');
    expect(within(section).queryByText('already answered')).toBeNull();
    expect(within(section).queryByText('rate limited')).toBeNull();

    const urls = fetchMock.mock.calls.map(([u]) => u);
    expect(urls).toContain('/hub/api/v1/projects');
    expect(urls).toContain('/hub/p/alpha/api/v1/state');
    expect(urls.some((u) => u.includes('/hub/p/beta/'))).toBe(false); // nothing waiting
    expect(urls.some((u) => u.includes('/hub/p/legacy/'))).toBe(false); // not proxied
    expect(screen.getByRole('tab', { name: /Needs you/ }).textContent).toContain('2');
  });

  it('says nothing needs you, and switches to the projects view', async () => {
    global.fetch = vi.fn(() =>
      Promise.resolve(
        new Response(JSON.stringify({ ...PROJECTS, projects: [project('beta', { running: 2 })] }), {
          status: 200,
        }),
      ),
    ) as unknown as typeof fetch;
    renderPage();
    expect(await screen.findByTestId('attention-empty')).toBeTruthy();
    fireEvent.click(screen.getByRole('tab', { name: 'Projects' }));
    const list = await screen.findByRole('list', { name: 'Projects' });
    expect(within(list).getByText('beta')).toBeTruthy();
  });

  it('reads only proxied projects with something waiting', () => {
    const parsed = HubProjectsResponseSchema.parse(PROJECTS);
    expect(projectsNeedingAttention(parsed.projects).map((p) => p.name)).toEqual(['alpha']);
  });

  it('sends hub calls to a configured API origin', async () => {
    configureApiOrigin('https://hub.example.ts.net');
    const fetchMock = vi.fn(() =>
      Promise.resolve(new Response(JSON.stringify(PROJECTS), { status: 200 })),
    );
    global.fetch = fetchMock as unknown as typeof fetch;
    renderPage();
    await screen.findByRole('region', { name: 'Needs attention' });
    await vi.waitFor(() => {
      expect(fetchMock.mock.calls.map(([u]) => u as string)).toContain(
        'https://hub.example.ts.net/hub/p/alpha/api/v1/state',
      );
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe('https://hub.example.ts.net/hub/api/v1/projects');
  });
});
