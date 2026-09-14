import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import { useRetryOutboxEntry, useDiscardOutboxEntry } from '../outbox';

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
  useItervoxStore.setState({
    refreshSnapshot: vi.fn().mockResolvedValue(undefined),
  });
});

describe('useRetryOutboxEntry', () => {
  it('POSTs /api/v1/outbox/{id}/retry and refreshes the snapshot on success', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ id: 'entry-1', retried: true }), { status: 202 }),
    );

    const { result } = renderHook(() => useRetryOutboxEntry(), { wrapper });
    act(() => {
      result.current.mutate('entry-1');
    });

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });

    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/outbox/entry-1/retry',
      expect.objectContaining({ method: 'POST' }),
    );
    expect(useItervoxStore.getState().refreshSnapshot).toHaveBeenCalled();
  });

  it('surfaces a toast and does not refresh on a 404', async () => {
    mockAuthedFetch.mockResolvedValueOnce(new Response('not found', { status: 404 }));

    const { result } = renderHook(() => useRetryOutboxEntry(), { wrapper });
    act(() => {
      result.current.mutate('missing');
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });

    expect(useItervoxStore.getState().refreshSnapshot).not.toHaveBeenCalled();
    expect(
      useToastStore.getState().toasts.some((t) => t.message.toLowerCase().includes('retry failed')),
    ).toBe(true);
  });
});

describe('useDiscardOutboxEntry', () => {
  it('DELETEs /api/v1/outbox/{id} and refreshes the snapshot on success', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ id: 'entry-1', dropped: true }), { status: 202 }),
    );

    const { result } = renderHook(() => useDiscardOutboxEntry(), { wrapper });
    act(() => {
      result.current.mutate('entry-1');
    });

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });

    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/outbox/entry-1',
      expect.objectContaining({ method: 'DELETE' }),
    );
    expect(useItervoxStore.getState().refreshSnapshot).toHaveBeenCalled();
  });
});
