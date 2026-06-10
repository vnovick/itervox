import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import { useAnalyzeDeps } from '../deps';

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

beforeEach(() => {
  mockAuthedFetch.mockReset();
  useToastStore.setState({ toasts: [] });
  // Stub refreshSnapshot so success path doesn't hit the network.
  useItervoxStore.setState({
    refreshSnapshot: vi.fn().mockResolvedValue(undefined),
  });
});

afterEach(() => {
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
  status: 'queued' | 'running' | 'succeeded' | 'failed',
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

describe('useAnalyzeDeps', () => {
  it('POSTs the request body and resolves on a succeeded poll', async () => {
    mockAuthedFetch
      .mockResolvedValueOnce(enqueuedResponse('job-1'))
      .mockResolvedValueOnce(statusResponse('running'))
      .mockResolvedValueOnce(statusResponse('succeeded', { edgesFound: 4 }));

    const { result } = renderHook(() => useAnalyzeDeps(), { wrapper });
    result.current.mutate({ profile: 'deps-analyzer' });

    // Real timers: the polling loop runs at 1s then 2s. Allow up to 8s for the
    // running → succeeded transition to land.
    await waitFor(
      () => {
        expect(result.current.isSuccess).toBe(true);
      },
      { timeout: 8_000 },
    );
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
    result.current.mutate({});

    await waitFor(
      () => {
        expect(result.current.isError).toBe(true);
      },
      { timeout: 5_000 },
    );
    expect(
      useToastStore.getState().toasts.some((t) => t.message.toLowerCase().includes('rate limited')),
    ).toBe(true);
  });

  it('surfaces the server error body when the POST itself fails', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response('analyzer profile not configured', { status: 422 }),
    );

    const { result } = renderHook(() => useAnalyzeDeps(), { wrapper });
    result.current.mutate({});

    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(
      useToastStore
        .getState()
        .toasts.some((t) => t.message.toLowerCase().includes('analyzer profile not configured')),
    ).toBe(true);
  });
});
