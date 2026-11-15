import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { HubNotificationSettings } from '../HubNotificationSettings';
import { configureApiContext } from '../../../auth/apiBase';
import { useToastStore } from '../../../store/toastStore';

const ALL = ['push', 'slack', 'desktop'];
const RESPONSE = {
  settings: {
    $schema: 'https://itervox.dev/schema/notifications.schema.json',
    version: 1,
    digest_interval_minutes: 15,
    channels: { push: { enabled: true }, slack: { enabled: true }, desktop: { enabled: true } },
    defaults: {
      input_required: ALL,
      guardrail_approval: ALL,
      run_failed: ALL,
      review_verdict: [],
      pr_merged: [],
      lesson_proposed: [],
      dispatch_paused: [],
    },
    projects: {},
  },
  kinds: [
    { name: 'input_required', urgent: true },
    { name: 'guardrail_approval', urgent: true },
    { name: 'run_failed', urgent: false },
    { name: 'pr_merged', urgent: false },
  ],
  channels: [
    { name: 'push', available: false },
    { name: 'slack', available: false },
    { name: 'desktop', available: true },
  ],
  projects: ['alpha'],
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

function renderSettings() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <HubNotificationSettings />
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

// #229 — the hub's Notifications settings view.
describe('HubNotificationSettings', () => {
  it('shows the project × kind × channel matrix and saves a changed route', async () => {
    const fetchMock = vi.fn((_url: string, init?: RequestInit) =>
      Promise.resolve(
        init?.method === 'PUT'
          ? json({ ...RESPONSE, settings: JSON.parse(init.body as string) })
          : json(RESPONSE),
      ),
    );
    global.fetch = fetchMock as unknown as typeof fetch;
    renderSettings();
    const table = await screen.findByRole('table', { name: 'alpha notification routes' });
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/hub/api/v1/notifications');
    // Defaults: urgent kinds and run failed on every channel; the rest off.
    const runFailedSlack = within(table).getByRole('checkbox', {
      name: 'alpha Run failed via Slack',
    });
    const prDesktop = within(table).getByRole('checkbox', { name: 'alpha PR merged via Desktop' });
    expect((runFailedSlack as HTMLInputElement).checked).toBe(true);
    expect((prDesktop as HTMLInputElement).checked).toBe(false);
    expect(within(table).getAllByText('(urgent)')).toHaveLength(2);

    const save = screen.getByRole('button', { name: 'Save' });
    expect((save as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(prDesktop);
    fireEvent.click(runFailedSlack);
    expect((prDesktop as HTMLInputElement).checked).toBe(true);
    fireEvent.click(save);
    await waitFor(() => {
      expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(true);
    });
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === 'PUT');
    expect(put?.[0]).toBe('/hub/api/v1/notifications');
    const sent = JSON.parse(put?.[1]?.body as string) as typeof RESPONSE.settings & {
      projects: Record<string, { routes: Record<string, string[]> }>;
    };
    expect(sent.projects.alpha.routes).toEqual({
      pr_merged: ['desktop'],
      run_failed: ['push', 'desktop'],
    });
  });

  it('snoozes a project and changes the digest interval', async () => {
    const fetchMock = vi.fn((_url: string, init?: RequestInit) =>
      Promise.resolve(
        init?.method === 'PUT'
          ? json({ ...RESPONSE, settings: JSON.parse(init.body as string) })
          : json(RESPONSE),
      ),
    );
    global.fetch = fetchMock as unknown as typeof fetch;
    renderSettings();
    fireEvent.change(await screen.findByRole('combobox', { name: 'Snooze alpha' }), {
      target: { value: '60' },
    });
    expect(screen.getByText(/Snoozed until/)).toBeTruthy();
    fireEvent.change(screen.getByRole('spinbutton', { name: /Digest interval/ }), {
      target: { value: '30' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => {
      expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(true);
    });
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === 'PUT');
    const sent = JSON.parse(put?.[1]?.body as string) as {
      digest_interval_minutes: number;
      projects: Record<string, { snooze_until?: string }>;
    };
    expect(sent.digest_interval_minutes).toBe(30);
    const until = Date.parse(sent.projects.alpha.snooze_until ?? '');
    expect(until - Date.now()).toBeGreaterThan(55 * 60_000);
    expect(until - Date.now()).toBeLessThanOrEqual(60 * 60_000);
  });

  it('sends a test notification through an available channel only', async () => {
    const addToast = vi.fn();
    vi.spyOn(useToastStore, 'getState').mockReturnValue({
      ...useToastStore.getState(),
      addToast,
    });
    const fetchMock = vi.fn((url: string) =>
      Promise.resolve(
        url.endsWith('/test') ? json({ sent: true, channel: 'desktop' }) : json(RESPONSE),
      ),
    );
    global.fetch = fetchMock as unknown as typeof fetch;
    renderSettings();
    const desktop = await screen.findByRole('button', { name: 'Send test Desktop' });
    const slack = screen.getByRole('button', { name: 'Send test Slack' });
    expect((slack as HTMLButtonElement).disabled).toBe(true);
    expect(slack.getAttribute('title')).toBe('Slack is not set up');
    fireEvent.click(desktop);
    await waitFor(() => {
      expect(addToast).toHaveBeenCalledWith('Test notification sent via Desktop', 'success');
    });
    const post = fetchMock.mock.calls.find(([url]) => url.endsWith('/test')) as
      | [string, RequestInit]
      | undefined;
    expect(post?.[0]).toBe('/hub/api/v1/notifications/test');
    expect(post?.[1].method).toBe('POST');
    expect(JSON.parse(post?.[1].body as string)).toEqual({ channel: 'desktop' });
  });

  it('explains a refusal (an admin token is needed)', async () => {
    global.fetch = vi.fn(() =>
      Promise.resolve(
        json(
          { error: { code: 'insufficient_scope', message: 'this action needs the admin scope' } },
          403,
        ),
      ),
    ) as unknown as typeof fetch;
    renderSettings();
    expect((await screen.findByRole('alert')).textContent).toContain('admin');
  });
});
