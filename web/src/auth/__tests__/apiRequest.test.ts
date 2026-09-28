import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { ApiError, apiRequest } from '../apiRequest';
import { UnauthorizedError } from '../UnauthorizedError';

// CORE-049 — one request helper over authedFetch: typed ApiError carrying the
// server's {code,message,field}, status exposed on success, a single retry on
// an admission-rejected 503 (CORE-005 / CORE-170), UnauthorizedError
// propagated, non-JSON bodies tolerated.

let fetchMock: ReturnType<typeof vi.fn>;

function envelope(status: number, code: string, message: string, headers?: HeadersInit): Response {
  return new Response(JSON.stringify({ error: { code, message } }), { status, headers });
}

beforeEach(() => {
  fetchMock = vi.fn();
  global.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('apiRequest', () => {
  it('apiRequest surfaces server message', async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          error: { code: 'bad_request', message: 'unknown backend "gemini"', field: 'backend' },
        }),
        { status: 400 },
      ),
    );
    const err = await apiRequest('/api/v1/x', { op: 'setBackend', method: 'POST', json: {} }).catch(
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(ApiError);
    const apiErr = err as ApiError;
    expect(apiErr.message).toBe('unknown backend "gemini"');
    expect(apiErr.code).toBe('bad_request');
    expect(apiErr.field).toBe('backend');
    expect(apiErr.status).toBe(400);
  });

  it('falls back to "<op> failed: <status>" for a non-JSON body', async () => {
    fetchMock.mockResolvedValue(new Response('<html>502</html>', { status: 502 }));
    await expect(apiRequest('/api/v1/x', { op: 'thing' })).rejects.toMatchObject({
      message: 'thing failed: 502',
      status: 502,
      code: undefined,
    });
  });

  it('exposes status and parsed JSON on success, and serialises json bodies', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ queued: true }), { status: 202 }));
    const res = await apiRequest<{ queued: boolean }>('/api/v1/x', {
      op: 'post',
      method: 'POST',
      json: { a: 1 },
    });
    expect(res.status).toBe(202);
    expect(res.data).toEqual({ queued: true });
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.body).toBe('{"a":1}');
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json');
  });

  it('returns undefined data for an empty success body', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    const res = await apiRequest('/api/v1/x', { op: 'del', method: 'DELETE' });
    expect(res.status).toBe(204);
    expect(res.data).toBeUndefined();
  });

  it.each(['orchestrator_busy', 'settings_reloading'])(
    'retries exactly once on a 503 %s, honouring Retry-After',
    async (code) => {
      vi.useFakeTimers();
      fetchMock
        .mockResolvedValueOnce(envelope(503, code, 'busy', { 'Retry-After': '2' }))
        .mockResolvedValueOnce(new Response('{}', { status: 200 }));
      const p = apiRequest('/api/v1/x', { op: 'save', method: 'PUT', json: {} });
      await vi.advanceTimersByTimeAsync(1_900);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(200);
      await expect(p).resolves.toMatchObject({ status: 200 });
      expect(fetchMock).toHaveBeenCalledTimes(2);
    },
  );

  it('never retries a 503 without an admission-rejected code', async () => {
    fetchMock.mockResolvedValue(envelope(503, 'deps_analyzer_unavailable', 'no analyzer'));
    await expect(apiRequest('/api/v1/x', { op: 'analyze' })).rejects.toMatchObject({
      status: 503,
      code: 'deps_analyzer_unavailable',
    });
    fetchMock.mockClear();
    fetchMock.mockResolvedValue(new Response('', { status: 503 }));
    await expect(apiRequest('/api/v1/x', { op: 'analyze' })).rejects.toMatchObject({ status: 503 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('can opt out of the 503 retry', async () => {
    fetchMock.mockResolvedValue(envelope(503, 'orchestrator_busy', 'busy', { 'Retry-After': '0' }));
    await expect(apiRequest('/api/v1/x', { op: 'x', retryOnBusy: false })).rejects.toBeInstanceOf(
      ApiError,
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('propagates UnauthorizedError untouched', async () => {
    fetchMock.mockResolvedValue(new Response('', { status: 401 }));
    await expect(apiRequest('/api/v1/x', { op: 'x' })).rejects.toBeInstanceOf(UnauthorizedError);
  });

  it('does not retry a transport failure', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
    await expect(apiRequest('/api/v1/x', { op: 'x' })).rejects.toBeInstanceOf(TypeError);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
