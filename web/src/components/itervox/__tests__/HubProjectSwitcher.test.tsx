import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { HubProjectSwitcher } from '../HubProjectSwitcher';
import { configureApiContext } from '../../../auth/apiBase';

const PROJECTS = {
  hub: { version: 'v', pid: 1, url: '', startedAt: '', headSeq: 0 },
  projects: ['alpha', 'beta'].map((name, i) => ({
    name,
    workflow: `/w/${name}/WORKFLOW.md`,
    projectDir: `/w/${name}`,
    pid: i + 1,
    url: '',
    version: 'v',
    startedAt: '',
    health: 'ok',
    proxied: true,
    capabilities: [],
  })),
};

function renderSwitcher() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <HubProjectSwitcher />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  configureApiContext('/');
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// #228 — the project switcher inside a hub-served project dashboard.
describe('HubProjectSwitcher', () => {
  it('renders and loads nothing on a daemon', async () => {
    configureApiContext('/');
    const fetchMock = vi.fn();
    global.fetch = fetchMock as unknown as typeof fetch;
    const { container } = renderSwitcher();
    await expect(
      screen.findByRole('combobox', { name: 'Switch project' }, { timeout: 300 }),
    ).rejects.toThrow();
    expect(container.innerHTML).toBe('');
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('switches to another project or back to the hub', async () => {
    configureApiContext('/hub/p/alpha/settings');
    global.fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify(PROJECTS), { status: 200 }),
      ) as unknown as typeof fetch;
    const assign = vi.fn();
    vi.stubGlobal('location', {
      assign,
      pathname: '/hub/p/alpha/settings',
      origin: 'http://localhost',
    });
    renderSwitcher();
    const select = await screen.findByRole('combobox', { name: 'Switch project' });
    await screen.findByRole('option', { name: 'beta' });
    expect((select as HTMLSelectElement).value).toBe('alpha');
    fireEvent.change(select, { target: { value: 'beta' } });
    expect(assign).toHaveBeenCalledWith('/hub/p/beta/');
    fireEvent.change(select, { target: { value: '' } });
    expect(assign).toHaveBeenCalledWith('/hub/');
  });
});
