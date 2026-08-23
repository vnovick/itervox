import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import { useAnalyzeDeps, useCancelAnalyzeDeps, useSetDepsOverride } from '../deps';

vi.mock('../../auth/authedFetch', () => ({
  authedFetch: vi.fn(),
}));

import { authedFetch } from '../../auth/authedFetch';
import { useToastStore } from '../../store/toastStore';
import { useItervoxStore } from '../../store/itervoxStore';

const mockAuthedFetch = vi.mocked(authedFetch);

function wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return React.createElement(QueryClientProvider, { client: qc }, children);
}

// Fake timers make the polling tests deterministic: deps.ts's polling loop
// (web/src/queries/deps.ts) uses setTimeout-based exponential backoff with a
// 10-minute wall-clock ceiling. Driving that loop with real timers meant
// these tests' pass/fail depended on actual elapsed time — under machine
// contention a real setTimeout(1000) can take much longer than 1000ms to
// fire, which flaked "surfaces the analyzer error message on a failed poll"
// under load even though nothing in the polling logic had changed. Fake
// timers plus vi.advanceTimersByTimeAsync remove wall-clock from the
// equation entirely: we advance exactly the delays the loop itself uses.
beforeEach(() => {
  // Only fake setTimeout/Date — deps.ts's polling loop is the only thing we
  // want deterministic control over. Leaving setImmediate/microtasks real
  // avoids stalling unrelated async plumbing (e.g. Response body reads)
  // that some environments schedule via setImmediate.
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
  mockAuthedFetch.mockReset();
  useToastStore.setState({ toasts: [] });
  // Stub refreshSnapshot so success path doesn't hit the network.
  useItervoxStore.setState({
    refreshSnapshot: vi.fn().mockResolvedValue(undefined),
  });
});

afterEach(() => {
  vi.useRealTimers();
  vi.clearAllMocks();
});

const enqueuedResponse = (jobId: string) =>
  new Response(
    JSON.stringify({
      jobId,
      profile: 'deps-analyzer',
      queuedAt: new Date(0).toISOString(),
    }),
    { status: 202 },
  );

const statusResponse = (
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled',
  extra?: Record<string, unknown>,
) =>
  new Response(
    JSON.stringify({
      jobId: 'job-1',
      profile: 'deps-analyzer',
      status,
      queuedAt: new Date(0).toISOString(),
      ...extra,
    }),
    { status: 200 },
  );

// Under fake timers, React 19's scheduler still commits the post-mutation
// re-render via a real macrotask (a `setTimeout`, since jsdom's
// MessageChannel path isn't available here) rather than a plain microtask.
// `advanceTimersToNextTimerAsync` drains exactly that one pending scheduler
// tick so `result.current` reflects the settled mutation before we assert on
// it — without this, `result.current.status` can still read `pending` even
// after the mutation's own promise has resolved. Deliberately NOT
// `runAllTimersAsync`: that drains every pending timer including the toast
// store's multi-second auto-dismiss, which would delete the toast these
// tests assert on before the assertion runs.
async function flushMutation(mutation: Promise<unknown>): Promise<void> {
  await act(async () => {
    await mutation;
    await vi.advanceTimersToNextTimerAsync();
  });
}

describe('useAnalyzeDeps', () => {
  it('POSTs the request body and resolves on a succeeded poll', async () => {
    mockAuthedFetch
      .mockResolvedValueOnce(enqueuedResponse('job-1'))
      .mockResolvedValueOnce(statusResponse('running'))
      .mockResolvedValueOnce(statusResponse('succeeded', { edgesFound: 4 }));

    const { result } = renderHook(() => useAnalyzeDeps(), { wrapper });
    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current.mutateAsync({ profile: 'deps-analyzer' });
    });

    // Poll cadence: 1s, then 2s (delay doubles after the first tick, capped
    // at 5s — see POLL_INTERVAL_START_MS / POLL_INTERVAL_MAX_MS in deps.ts).
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000); // unlocks the 'running' check
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000); // unlocks the 'succeeded' check
    });

    await flushMutation(mutation);

    expect(result.current.isSuccess).toBe(true);
    expect(mockAuthedFetch).toHaveBeenNthCalledWith(
      1,
      '/api/v1/deps/analyze',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ profile: 'deps-analyzer' }),
      }),
    );
    expect(
      useToastStore
        .getState()
        .toasts.some((t) => t.message.toLowerCase().includes('analyzed dependencies')),
    ).toBe(true);
    expect(useItervoxStore.getState().refreshSnapshot).toHaveBeenCalled();
  });

  it('surfaces the analyzer error message on a failed poll', async () => {
    mockAuthedFetch
      .mockResolvedValueOnce(enqueuedResponse('job-1'))
      .mockResolvedValueOnce(statusResponse('failed', { error: 'rate limited' }));

    const { result } = renderHook(() => useAnalyzeDeps(), { wrapper });
    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current.mutateAsync({}).catch((err: unknown) => err);
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000); // unlocks the 'failed' check
    });

    await flushMutation(mutation);

    expect(result.current.isError).toBe(true);
    expect(
      useToastStore.getState().toasts.some((t) => t.message.toLowerCase().includes('rate limited')),
    ).toBe(true);
  });

  it('surfaces the server error body when the POST itself fails', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response('analyzer profile not configured', { status: 422 }),
    );

    const { result } = renderHook(() => useAnalyzeDeps(), { wrapper });
    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current.mutateAsync({}).catch((err: unknown) => err);
    });
    await flushMutation(mutation);

    expect(result.current.isError).toBe(true);
    expect(
      useToastStore
        .getState()
        .toasts.some((t) => t.message.toLowerCase().includes('analyzer profile not configured')),
    ).toBe(true);
  });

  it('treats a cancelled job as terminal rather than an error', async () => {
    mockAuthedFetch
      .mockResolvedValueOnce(enqueuedResponse('job-1'))
      .mockResolvedValueOnce(statusResponse('cancelled'));

    const { result } = renderHook(() => useAnalyzeDeps(), { wrapper });
    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current.mutateAsync({});
    });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000); // unlocks the 'cancelled' check
    });

    await flushMutation(mutation);

    // Resolves rather than throwing, and stops polling on the cancelled status.
    expect(result.current.isError).toBe(false);
    expect(result.current.data?.status).toBe('cancelled');
    // An operator-initiated cancel is not a failure — no error toast.
    expect(useToastStore.getState().toasts.some((t) => t.variant === 'error')).toBe(false);
  });
});

describe('useCancelAnalyzeDeps', () => {
  it('sends DELETE to the cancel endpoint', async () => {
    mockAuthedFetch.mockResolvedValueOnce(new Response(null, { status: 204 }));

    const { result } = renderHook(() => useCancelAnalyzeDeps(), { wrapper });
    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current.mutateAsync({ jobId: 'job-1' });
    });
    await flushMutation(mutation);

    expect(result.current.isSuccess).toBe(true);
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/deps/analyze/job-1',
      expect.objectContaining({ method: 'DELETE' }),
    );
  });

  it('does not surface a 404 cancel as an error', async () => {
    // The job finished between the operator clicking and the request
    // landing — a normal race, not something to toast about.
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ error: 'deps_analyze_job_not_running' }), { status: 404 }),
    );

    const { result } = renderHook(() => useCancelAnalyzeDeps(), { wrapper });
    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current.mutateAsync({ jobId: 'gone' });
    });
    await flushMutation(mutation);

    expect(result.current.isError).toBe(false);
    expect(result.current.isSuccess).toBe(true);
  });
});

// unified-dependency-graph Task 8 — dismiss/restore the inferred dependency
// gating layer for one issue via POST/DELETE deps-override.
describe('useSetDepsOverride', () => {
  it('POSTs deps-override and refreshes the snapshot on the dismiss path', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ identifier: 'ENG-2', overridden: true }), { status: 202 }),
    );

    const { result } = renderHook(() => useSetDepsOverride(), { wrapper });
    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current.mutateAsync({ identifier: 'ENG-2', enabled: true });
    });
    await flushMutation(mutation);

    expect(result.current.isSuccess).toBe(true);
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/issues/ENG-2/deps-override',
      expect.objectContaining({ method: 'POST' }),
    );
    expect(useItervoxStore.getState().refreshSnapshot).toHaveBeenCalled();
    expect(
      useToastStore
        .getState()
        .toasts.some((t) => t.message.toLowerCase().includes('dismissed inferred blockers')),
    ).toBe(true);
  });

  it('DELETEs deps-override and refreshes the snapshot on the restore path', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ identifier: 'ENG-2', overridden: false }), { status: 202 }),
    );

    const { result } = renderHook(() => useSetDepsOverride(), { wrapper });
    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current.mutateAsync({ identifier: 'ENG-2', enabled: false });
    });
    await flushMutation(mutation);

    expect(result.current.isSuccess).toBe(true);
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/issues/ENG-2/deps-override',
      expect.objectContaining({ method: 'DELETE' }),
    );
    expect(useItervoxStore.getState().refreshSnapshot).toHaveBeenCalled();
    expect(
      useToastStore
        .getState()
        .toasts.some((t) => t.message.toLowerCase().includes('restored inferred blockers')),
    ).toBe(true);
  });

  it('surfaces a failed override as an error toast without calling refreshSnapshot', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response('orchestrator event queue is full; retry', { status: 503 }),
    );

    const { result } = renderHook(() => useSetDepsOverride(), { wrapper });
    let mutation!: Promise<unknown>;
    act(() => {
      mutation = result.current
        .mutateAsync({ identifier: 'ENG-2', enabled: true })
        .catch((err: unknown) => err);
    });
    await flushMutation(mutation);

    expect(result.current.isError).toBe(true);
    expect(useItervoxStore.getState().refreshSnapshot).not.toHaveBeenCalled();
    expect(
      useToastStore
        .getState()
        .toasts.some(
          (t) => t.variant === 'error' && t.message.toLowerCase().includes('queue is full'),
        ),
    ).toBe(true);
  });
});
