import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const authedFetch = vi.fn();
vi.mock('../../auth/authedFetch', () => ({
  authedFetch: (...args: unknown[]) => authedFetch(...args) as Promise<Response>,
}));

import {
  reportClientError,
  installGlobalErrorReporting,
  __resetClientErrorReporterForTest,
  CLIENT_ERROR_DEDUPE_MS,
  CLIENT_ERROR_MAX_PER_MINUTE,
  CLIENT_ERROR_STACK_MAX,
} from '../clientErrorReporter';
import { useAuthStore } from '../../auth/authStore';
import { StateSnapshotSchema } from '../../types/schemas';

function sentBodies(): Record<string, unknown>[] {
  return authedFetch.mock.calls.map(
    (c) => JSON.parse((c[1] as RequestInit).body as string) as Record<string, unknown>,
  );
}

beforeEach(() => {
  vi.useFakeTimers();
  __resetClientErrorReporterForTest();
  authedFetch.mockReset();
  authedFetch.mockResolvedValue(new Response('{"accepted":true}', { status: 202 }));
  useAuthStore.setState({ status: 'authorized' });
  window.history.replaceState(null, '', '/settings');
});

afterEach(() => {
  vi.useRealTimers();
});

describe('clientErrorReporter', () => {
  it('posts only kind, message, route and a truncated stack via authedFetch', () => {
    reportClientError({ kind: 'render', message: 'boom', stack: 'x'.repeat(10_000) });
    expect(authedFetch).toHaveBeenCalledTimes(1);
    const [url, init] = authedFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/client-errors');
    expect(init.method).toBe('POST');
    const body = sentBodies()[0];
    expect(Object.keys(body).sort()).toEqual(['kind', 'message', 'route', 'stack']);
    expect(body.route).toBe('/settings');
    expect((body.stack as string).length).toBeLessThanOrEqual(CLIENT_ERROR_STACK_MAX);
  });

  it('dedupes identical reports within 60s', () => {
    reportClientError({ kind: 'error', message: 'same' });
    reportClientError({ kind: 'error', message: 'same' });
    expect(authedFetch).toHaveBeenCalledTimes(1);
    reportClientError({ kind: 'error', message: 'different' });
    expect(authedFetch).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(CLIENT_ERROR_DEDUPE_MS + 1);
    reportClientError({ kind: 'error', message: 'same' });
    expect(authedFetch).toHaveBeenCalledTimes(3);
  });

  it('throttles a storm of distinct reports', () => {
    for (let i = 0; i < CLIENT_ERROR_MAX_PER_MINUTE + 10; i++) {
      reportClientError({ kind: 'error', message: `distinct ${String(i)}` });
    }
    expect(authedFetch).toHaveBeenCalledTimes(CLIENT_ERROR_MAX_PER_MINUTE);
    vi.advanceTimersByTime(60_001);
    reportClientError({ kind: 'error', message: 'after the window' });
    expect(authedFetch).toHaveBeenCalledTimes(CLIENT_ERROR_MAX_PER_MINUTE + 1);
  });

  it('skips while the auth gate is needsToken or serverDown', () => {
    useAuthStore.setState({ status: 'needsToken' });
    reportClientError({ kind: 'error', message: 'a' });
    useAuthStore.setState({ status: 'serverDown' });
    reportClientError({ kind: 'error', message: 'b' });
    expect(authedFetch).not.toHaveBeenCalled();
    // allow_unauthenticated sessions sit at 'authorized' with no token and still report.
    useAuthStore.setState({ status: 'authorized' });
    reportClientError({ kind: 'error', message: 'c' });
    expect(authedFetch).toHaveBeenCalledTimes(1);
  });

  it('never reports its own failures', async () => {
    const onError = vi.fn();
    window.addEventListener('unhandledrejection', onError);
    authedFetch.mockRejectedValue(new Error('network down'));
    const uninstall = installGlobalErrorReporting();
    reportClientError({ kind: 'error', message: 'first' });
    await vi.runAllTimersAsync();
    expect(authedFetch).toHaveBeenCalledTimes(1);
    uninstall();
    window.removeEventListener('unhandledrejection', onError);
  });

  it('reports window error and unhandledrejection events once installed', () => {
    const uninstall = installGlobalErrorReporting();
    window.dispatchEvent(
      new ErrorEvent('error', { message: 'kaboom', error: new Error('kaboom') }),
    );
    const ev = new Event('unhandledrejection') as Event & { reason?: unknown };
    ev.reason = new Error('rejected promise');
    window.dispatchEvent(ev);
    const kinds = sentBodies().map((b) => `${String(b.kind)}:${String(b.message)}`);
    expect(kinds).toEqual(['error:kaboom', 'unhandledrejection:rejected promise']);
    uninstall();
    window.dispatchEvent(new ErrorEvent('error', { message: 'after uninstall' }));
    expect(authedFetch).toHaveBeenCalledTimes(2);
  });

  it('reports a snapshot enum fallback (CORE-047) as schema drift', () => {
    const uninstall = installGlobalErrorReporting();
    StateSnapshotSchema.parse({
      generatedAt: 'x',
      counts: { running: 0, retrying: 0, paused: 0 },
      running: [],
      retrying: [],
      paused: [],
      maxConcurrentAgents: 1,
      maxRetries: 1,
      maxSwitchesPerIssuePerWindow: 1,
      switchWindowHours: 1,
      rateLimits: null,
      depsAnalysisMode: 'hybrid',
    });
    const body = sentBodies()[0];
    expect(body.kind).toBe('schema');
    expect(body.message).toContain('depsAnalysisMode');
    uninstall();
  });
});

// BH-M2-3 — the server caps the body at 8 KiB of BYTES; UTF-16 truncation let
// a CJK/emoji-heavy report exceed it and get a silent 413.
describe('clientErrorReporter byte bounds', () => {
  const LONE_SURROGATE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/;

  it('keeps a 20 KiB CJK/emoji report within the 8 KiB server cap, without split surrogates', () => {
    const message = '界'.repeat(4_000) + '😀'.repeat(2_000); // ~20 KiB of UTF-8
    const stack = '\u0001'.repeat(3_000) + '😀'.repeat(1_000); // escapes expand 6×
    reportClientError({ kind: 'error', message, stack });
    expect(authedFetch).toHaveBeenCalledTimes(1);
    const raw = (authedFetch.mock.calls[0][1] as RequestInit).body as string;
    expect(new TextEncoder().encode(raw).byteLength).toBeLessThanOrEqual(8 * 1024);
    const body = JSON.parse(raw) as { message: string; stack: string };
    expect(body.message.length).toBeGreaterThan(0);
    expect(LONE_SURROGATE.test(body.message)).toBe(false);
    expect(LONE_SURROGATE.test(body.stack)).toBe(false);
  });

  it('releases the dedupe key when the daemon does not accept a report', async () => {
    authedFetch.mockResolvedValueOnce(new Response('{}', { status: 503 }));
    reportClientError({ kind: 'error', message: 'lost once' });
    await vi.advanceTimersByTimeAsync(0);
    reportClientError({ kind: 'error', message: 'lost once' });
    expect(authedFetch).toHaveBeenCalledTimes(2);
    // An accepted report stays deduped.
    await vi.advanceTimersByTimeAsync(0);
    reportClientError({ kind: 'error', message: 'lost once' });
    expect(authedFetch).toHaveBeenCalledTimes(2);
  });
});
