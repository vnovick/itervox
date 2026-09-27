import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useTokenStore } from '../tokenStore';
import { useAuthStore } from '../authStore';

// Mock fetchEventSource BEFORE importing openAuthedEventStream so the module
// picks up the mocked version at import time.
type OnOpen = (res: Response) => Promise<void> | undefined;
type OnMessage = (msg: { event?: string; data: string; id?: string; retry?: number }) => void;
type OnClose = () => void;
type OnError = (err: unknown) => number | undefined;

interface MockFetchEventSourceArgs {
  url: string;
  signal?: AbortSignal;
  headers?: Record<string, string>;
  openWhenHidden?: boolean;
  onopen?: OnOpen;
  onmessage?: OnMessage;
  onclose?: OnClose;
  onerror?: OnError;
}

const mockFetchEventSource = vi.fn();

vi.mock('@microsoft/fetch-event-source', () => ({
  fetchEventSource: (url: string, init: Omit<MockFetchEventSourceArgs, 'url'>) => {
    mockFetchEventSource({ url, ...init });
    return Promise.resolve();
  },
}));

// Must import AFTER vi.mock so the mocked dependency is used.
// vi.mock is hoisted above imports by Vitest at runtime, so the order here is safe.
import { openAuthedEventStream } from '../authedEventStream';
import { SSE_RECONNECT_BASE_MS, SSE_RECONNECT_MAX_MS } from '../../utils/timings';

beforeEach(() => {
  mockFetchEventSource.mockClear();
  useTokenStore.setState({ token: null });
  useAuthStore.setState({ status: 'authorized', rejectedReason: null });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('openAuthedEventStream', () => {
  it('calls fetchEventSource with the target URL', () => {
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    expect(mockFetchEventSource).toHaveBeenCalledTimes(1);
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    expect(call.url).toBe('/api/v1/events');
  });

  it('injects Authorization header when a token is stored', () => {
    useTokenStore.getState().setToken('abc123', false);
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    expect(call.headers).toEqual({ Authorization: 'Bearer abc123' });
  });

  it('sends an empty header object when no token is stored', () => {
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    expect(call.headers).toEqual({});
  });

  it('sets openWhenHidden: true to match native EventSource semantics', () => {
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    expect(call.openWhenHidden).toBe(true);
  });

  it('invokes opts.onOpen on a 200 response', async () => {
    const onOpen = vi.fn();
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined, onOpen });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    await call.onopen?.(new Response(null, { status: 200 }));
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it('clears the token and marks unauthorized on 401 at open', async () => {
    useTokenStore.getState().setToken('bad', false);
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;

    await expect(async () => {
      await call.onopen?.(new Response(null, { status: 401 }));
    }).rejects.toThrow(/unauthorized/);

    expect(useTokenStore.getState().token).toBeNull();
    // markUnauthorized is queued as a microtask; drain it.
    await Promise.resolve();
    expect(useAuthStore.getState().status).toBe('needsToken');
  });

  it('throws on non-401 non-2xx open so the library retries', async () => {
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    await expect(async () => {
      await call.onopen?.(new Response(null, { status: 503 }));
    }).rejects.toThrow(/sse open failed: 503/);
  });

  it('forwards onmessage to the caller', () => {
    const onMessage = vi.fn();
    openAuthedEventStream('/api/v1/events', { onMessage });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    call.onmessage?.({ event: 'log', data: 'hello' });
    expect(onMessage).toHaveBeenCalledWith({ event: 'log', data: 'hello' });
  });

  it('throws a retryable error from onclose on a clean close, without notifying directly', () => {
    const onDisconnect = vi.fn();
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined, onDisconnect });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    // onclose must throw so @microsoft/fetch-event-source's catch block
    // routes into onerror and retries — see CORE-004. It must NOT call
    // onDisconnect itself (onerror already does, so this would double-fire).
    expect(() => call.onclose?.()).toThrow();
    expect(onDisconnect).not.toHaveBeenCalled();
  });

  it('notifies onDisconnect exactly once when the onclose-thrown error reaches onerror', () => {
    const onDisconnect = vi.fn();
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined, onDisconnect });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    let thrown: unknown;
    try {
      call.onclose?.();
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(Error);
    // This is what @microsoft/fetch-event-source does internally: the
    // rejection from onclose is handed to onerror.
    call.onerror?.(thrown);
    expect(onDisconnect).toHaveBeenCalledTimes(1);
  });

  it('onclose after ctrl.abort() neither throws nor schedules a retry', () => {
    const onDisconnect = vi.fn();
    const close = openAuthedEventStream('/api/v1/events', {
      onMessage: () => undefined,
      onDisconnect,
    });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    close(); // aborts ctrl.signal — a deliberate unmount/navigation close.
    expect(call.signal?.aborted).toBe(true);
    expect(() => call.onclose?.()).not.toThrow();
    expect(onDisconnect).not.toHaveBeenCalled();
  });

  it('returns exponential backoff delays from onerror', () => {
    const onDisconnect = vi.fn();
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined, onDisconnect });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;

    // First error → 1000ms (base)
    const d1 = call.onerror?.(new Error('boom'));
    // Second error → 2000ms
    const d2 = call.onerror?.(new Error('boom'));
    // Third error → 4000ms
    const d3 = call.onerror?.(new Error('boom'));

    expect(d1).toBe(1000);
    expect(d2).toBe(2000);
    expect(d3).toBe(4000);
    // onDisconnect fires on every transient error.
    expect(onDisconnect).toHaveBeenCalledTimes(3);
  });

  // CORE-083 — the transport reads its policy from utils/timings, and the
  // shared constants carry the live 1 s / 15 s values (not the stale 5 s / 30 s).
  it('first reconnect delay is 1000ms and caps at 15000ms', () => {
    expect(SSE_RECONNECT_BASE_MS).toBe(1000);
    expect(SSE_RECONNECT_MAX_MS).toBe(15000);
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    const delays: (number | undefined)[] = [];
    for (let i = 0; i < 10; i++) delays.push(call.onerror?.(new Error('boom')));
    expect(delays[0]).toBe(SSE_RECONNECT_BASE_MS);
    expect(Math.max(...delays.map((d) => d ?? 0))).toBe(SSE_RECONNECT_MAX_MS);
  });

  it('caps backoff at 15s', () => {
    openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    // 20 consecutive errors — backoff must not exceed 15000ms
    let last: number | undefined;
    for (let i = 0; i < 20; i++) {
      last = call.onerror?.(new Error('boom'));
    }
    expect(last).toBe(15000);
  });

  it('returns a closer function that aborts the underlying fetch', () => {
    const close = openAuthedEventStream('/api/v1/events', { onMessage: () => undefined });
    const call = mockFetchEventSource.mock.calls[0][0] as MockFetchEventSourceArgs;
    expect(call.signal?.aborted).toBe(false);
    close();
    expect(call.signal?.aborted).toBe(true);
  });
});

// ─── Reconnect-after-clean-close, driven against the REAL library ─────────────
//
// The tests above mock @microsoft/fetch-event-source, so they can only assert
// what our wrapper calls — they cannot distinguish "onclose returns normally"
// (bug: library resolves and the stream ends for good, lib/esm/fetch.js:62-64)
// from "onclose throws" (fix: library's catch routes into onerror and
// schedules a retry). This block unmocks the library and drives a stubbed
// global fetch, so the assertion is against real library behavior.
describe('openAuthedEventStream — reconnects after server close (real library)', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.doUnmock('@microsoft/fetch-event-source');
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    // Restore the file-level mock for every other test in this file.
    vi.doMock('@microsoft/fetch-event-source', () => ({
      fetchEventSource: (url: string, init: Omit<MockFetchEventSourceArgs, 'url'>) => {
        mockFetchEventSource({ url, ...init });
        return Promise.resolve();
      },
    }));
  });

  function cleanEventStreamResponse(): Response {
    // A body that emits one message then ends the stream cleanly (no error) —
    // exactly what a graceful proxy/LB close or a handler return looks like.
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(new TextEncoder().encode('data: hello\n\n'));
        controller.close();
      },
    });
    return new Response(body, {
      status: 200,
      headers: { 'content-type': 'text/event-stream' },
    });
  }

  it('reconnects after server close', async () => {
    const fetchMock = vi.fn(() => Promise.resolve(cleanEventStreamResponse()));
    vi.stubGlobal('fetch', fetchMock);

    const { openAuthedEventStream: realOpenAuthedEventStream } =
      await import('../authedEventStream');
    const onDisconnect = vi.fn();
    realOpenAuthedEventStream('/api/v1/events', { onMessage: () => undefined, onDisconnect });

    // Drain the microtask queue so the first request's stream is read to
    // completion and onclose (which now throws) runs.
    await vi.advanceTimersByTimeAsync(0);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    // onDisconnect must fire exactly once for this clean close (via onerror,
    // not directly from onclose — see the mocked tests above).
    expect(onDisconnect).toHaveBeenCalledTimes(1);

    // Advance past the 1s backoff so the library's retry timer fires.
    await vi.advanceTimersByTimeAsync(1000);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
