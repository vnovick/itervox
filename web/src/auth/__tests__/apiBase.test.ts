import { afterEach, describe, expect, it, vi } from 'vitest';

const mockFetchEventSource = vi.fn();
vi.mock('@microsoft/fetch-event-source', () => ({
  fetchEventSource: (url: string) => {
    mockFetchEventSource(url);
    return Promise.resolve();
  },
}));

import {
  apiUrl,
  authProbeUrl,
  configureApiContext,
  DEFAULT_API_BASE,
  getApiContext,
  healthUrl,
  resolveApiContext,
  routerBasename,
} from '../apiBase';
import { authedFetch } from '../authedFetch';
import { openAuthedEventStream } from '../authedEventStream';

afterEach(() => {
  configureApiContext('/');
  vi.restoreAllMocks();
  mockFetchEventSource.mockClear();
});

// #228 — the API base path setting.
describe('resolveApiContext', () => {
  it('defaults to /api/v1 on a daemon', () => {
    expect(resolveApiContext('/')).toEqual({ mode: 'daemon', apiBase: '/api/v1', basename: '' });
    expect(resolveApiContext('/settings').apiBase).toBe(DEFAULT_API_BASE);
  });

  it('uses a valid build-time base and ignores an unsafe one', () => {
    expect(resolveApiContext('/', '/proxy/api/v1/').apiBase).toBe('/proxy/api/v1');
    expect(resolveApiContext('/', 'https://evil.example/api').apiBase).toBe('/api/v1');
    expect(resolveApiContext('/', '//evil.example/api').apiBase).toBe('/api/v1');
    expect(resolveApiContext('/', '/a/../b').apiBase).toBe('/api/v1');
  });

  it('serves a hub project under its proxied path with a router basename', () => {
    expect(resolveApiContext('/hub/p/alpha/settings')).toEqual({
      mode: 'hubProject',
      project: 'alpha',
      apiBase: '/hub/p/alpha/api/v1',
      basename: '/hub/p/alpha',
    });
    expect(resolveApiContext('/hub/p/web-3fa1c2').mode).toBe('hubProject');
  });

  it('treats /hub as the project switcher and refuses odd project names', () => {
    expect(resolveApiContext('/hub/').mode).toBe('hub');
    expect(resolveApiContext('/hub').apiBase).toBe('/hub/api/v1');
    expect(resolveApiContext('/hub/p/..%2Fx/').mode).toBe('daemon');
    expect(resolveApiContext('/hub/p/.hidden/').mode).toBe('daemon');
    expect(resolveApiContext('/hub/p/UPPER/').mode).toBe('daemon');
  });
});

describe('apiUrl and the probes', () => {
  it('maps only /api/v1 URLs onto the base', () => {
    configureApiContext('/hub/p/alpha/');
    expect(apiUrl('/api/v1/state')).toBe('/hub/p/alpha/api/v1/state');
    expect(apiUrl('/api/v1/issues?state=x')).toBe('/hub/p/alpha/api/v1/issues?state=x');
    expect(apiUrl('/api/v1')).toBe('/hub/p/alpha/api/v1');
    expect(apiUrl('/api/v10/x')).toBe('/api/v10/x');
    expect(apiUrl('/hub/api/v1/projects')).toBe('/hub/api/v1/projects');
    expect(routerBasename()).toBe('/hub/p/alpha');
    expect(healthUrl()).toBe('/hub/api/v1/health');
    expect(authProbeUrl()).toBe('/hub/p/alpha/api/v1/state');
  });

  it('probes the hub itself on the switcher page', () => {
    configureApiContext('/hub/');
    expect(getApiContext().mode).toBe('hub');
    expect(healthUrl()).toBe('/hub/api/v1/health');
    expect(authProbeUrl()).toBe('/hub/api/v1/whoami');
  });

  it('leaves a daemon unchanged', () => {
    configureApiContext('/logs/ENG-1');
    expect(apiUrl('/api/v1/state')).toBe('/api/v1/state');
    expect(healthUrl()).toBe('/api/v1/health');
    expect(authProbeUrl()).toBe('/api/v1/state');
    expect(routerBasename()).toBe('');
  });
});

describe('the transport uses the base', () => {
  it('authedFetch sends /api/v1 calls to the configured base', async () => {
    configureApiContext('/hub/p/alpha/');
    const fetchMock = vi.fn().mockResolvedValue(new Response('{}', { status: 200 }));
    global.fetch = fetchMock as unknown as typeof fetch;
    await authedFetch('/api/v1/issues');
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/hub/p/alpha/api/v1/issues');
  });

  it('openAuthedEventStream opens the stream under the configured base', () => {
    configureApiContext('/hub/p/alpha/');
    const close = openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    expect(mockFetchEventSource).toHaveBeenCalledWith('/hub/p/alpha/api/v1/events');
    close();
  });
});
