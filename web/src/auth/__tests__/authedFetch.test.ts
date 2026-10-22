import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { authedFetch } from '../authedFetch';
import { UnauthorizedError } from '../UnauthorizedError';
import { useTokenStore } from '../tokenStore';
import { useAuthStore } from '../authStore';
import { useToastStore } from '../../store/toastStore';

beforeEach(() => {
  useTokenStore.setState({ token: null });
  useAuthStore.setState({ status: 'authorized', rejectedReason: null });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('authedFetch', () => {
  it('does not add an Authorization header when no token is stored', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('ok', { status: 200 }));
    global.fetch = fetchMock as unknown as typeof fetch;

    await authedFetch('/api/v1/state');

    const call = fetchMock.mock.calls[0] as [RequestInfo, RequestInit];
    const headers = new Headers(call[1].headers);
    expect(headers.has('Authorization')).toBe(false);
  });

  it('injects Bearer token from tokenStore', async () => {
    useTokenStore.getState().setToken('my-token', false);
    const fetchMock = vi.fn().mockResolvedValue(new Response('ok', { status: 200 }));
    global.fetch = fetchMock as unknown as typeof fetch;

    await authedFetch('/api/v1/state');

    const call = fetchMock.mock.calls[0] as [RequestInfo, RequestInit];
    const headers = new Headers(call[1].headers);
    expect(headers.get('Authorization')).toBe('Bearer my-token');
  });

  it('preserves caller-provided headers', async () => {
    useTokenStore.getState().setToken('tok', false);
    const fetchMock = vi.fn().mockResolvedValue(new Response('ok', { status: 200 }));
    global.fetch = fetchMock as unknown as typeof fetch;

    await authedFetch('/api/v1/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    });

    const call = fetchMock.mock.calls[0] as [RequestInfo, RequestInit];
    const headers = new Headers(call[1].headers);
    expect(headers.get('Content-Type')).toBe('application/json');
    expect(headers.get('Authorization')).toBe('Bearer tok');
  });

  it('throws UnauthorizedError on 401 and clears token', async () => {
    useTokenStore.getState().setToken('bad', false);
    global.fetch = vi
      .fn()
      .mockResolvedValue(new Response('nope', { status: 401 })) as unknown as typeof fetch;

    await expect(authedFetch('/api/v1/state')).rejects.toBeInstanceOf(UnauthorizedError);
    expect(useTokenStore.getState().token).toBeNull();
  });

  it('flips authStore to needsToken after a 401', async () => {
    useTokenStore.getState().setToken('bad', false);
    global.fetch = vi
      .fn()
      .mockResolvedValue(new Response('nope', { status: 401 })) as unknown as typeof fetch;

    await expect(authedFetch('/api/v1/state')).rejects.toBeInstanceOf(UnauthorizedError);
    // queueMicrotask runs between the throw and the awaiter's resume.
    expect(useAuthStore.getState().status).toBe('needsToken');
  });

  it('passes through non-401 non-ok responses unchanged', async () => {
    global.fetch = vi
      .fn()
      .mockResolvedValue(new Response('boom', { status: 500 })) as unknown as typeof fetch;

    const res = await authedFetch('/api/v1/state');
    expect(res.status).toBe(500);
  });

  it('toasts the scope a 403 insufficient_scope needed and returns the response (#214)', async () => {
    useToastStore.setState({ toasts: [] });
    const body = JSON.stringify({
      error: { code: 'insufficient_scope', message: 'needs admin', required_scope: 'admin' },
    });
    global.fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(body, { status: 403, headers: { 'X-Itervox-Required-Scope': 'admin' } }),
      ) as unknown as typeof fetch;

    const res = await authedFetch('/api/v1/settings/workers', { method: 'POST' });

    expect(res.status).toBe(403);
    const toasts = useToastStore.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0]?.message).toContain('needs the admin scope');
    expect(toasts[0]?.variant).toBe('error');
  });

  it('does not toast a 403 without the required-scope header', async () => {
    useToastStore.setState({ toasts: [] });
    global.fetch = vi
      .fn()
      .mockResolvedValue(new Response('{}', { status: 403 })) as unknown as typeof fetch;

    const res = await authedFetch('/api/v1/state', { method: 'POST' });

    expect(res.status).toBe(403);
    expect(useToastStore.getState().toasts).toHaveLength(0);
  });
});
