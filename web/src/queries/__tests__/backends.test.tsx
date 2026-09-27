import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import { useClearBackendBreaker } from '../backends';

vi.mock('../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

import { authedFetch } from '../../auth/authedFetch';
import { useToastStore } from '../../store/toastStore';
import { useItervoxStore } from '../../store/itervoxStore';

// M3-close V1 — the operator clear-breaker action. The endpoint queues the
// clear on the event loop (202), so the snapshot is refreshed, never patched.

const mockAuthedFetch = vi.mocked(authedFetch);

function wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return React.createElement(QueryClientProvider, { client: qc }, children);
}

let refresh: ReturnType<typeof vi.fn>;
beforeEach(() => {
  mockAuthedFetch.mockReset();
  useToastStore.setState({ toasts: [] });
  refresh = vi.fn().mockResolvedValue(undefined);
  useItervoxStore.setState({ refreshSnapshot: refresh });
});

describe('useClearBackendBreaker', () => {
  it('POSTs /api/v1/backend-health/clear and refreshes the snapshot', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ queued: true }), { status: 202 }),
    );
    const { result } = renderHook(() => useClearBackendBreaker(), { wrapper });
    act(() => {
      result.current.mutate({ backend: 'claude', host: 'build-1' });
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    const [url, init] = mockAuthedFetch.mock.calls[0];
    expect(url).toBe('/api/v1/backend-health/clear');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBe(JSON.stringify({ backend: 'claude', host: 'build-1' }));
    expect(refresh).toHaveBeenCalled();
  });

  it('toasts the daemon message on failure', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          error: { code: 'event_queue_full', message: 'orchestrator event queue is full; retry' },
        }),
        { status: 503 },
      ),
    );
    const { result } = renderHook(() => useClearBackendBreaker(), { wrapper });
    act(() => {
      result.current.mutate({ backend: 'codex' });
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(useToastStore.getState().toasts.map((t) => t.message)).toContain(
      'Clearing the codex breaker failed: orchestrator event queue is full; retry',
    );
    expect(refresh).not.toHaveBeenCalled();
  });
});
