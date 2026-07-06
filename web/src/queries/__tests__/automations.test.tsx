import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import { useTestAutomation } from '../automations';

// authedFetch is the seam; mock it so we can inspect the network call.
vi.mock('../../auth/authedFetch', () => ({
  authedFetch: vi.fn(),
}));

import { authedFetch } from '../../auth/authedFetch';
import { useToastStore } from '../../store/toastStore';

const mockAuthedFetch = vi.mocked(authedFetch);

function wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return React.createElement(QueryClientProvider, { client: qc }, children);
}

beforeEach(() => {
  mockAuthedFetch.mockReset();
  // Clear toast queue between tests.
  useToastStore.setState({ toasts: [] });
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('useTestAutomation (T-10)', () => {
  it('POSTs to /api/v1/automations/:id/test with the identifier in the body', async () => {
    mockAuthedFetch.mockResolvedValueOnce(new Response('{"ok":true}', { status: 200 }));
    const { result } = renderHook(() => useTestAutomation(), { wrapper });

    result.current.mutate({ automationId: 'pr-on-input', identifier: 'ENG-1' });

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/automations/pr-on-input/test',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ identifier: 'ENG-1' }),
      }),
    );
    // Success toast emitted.
    expect(
      useToastStore.getState().toasts.some((t) => t.message.toLowerCase().includes('dispatched')),
    ).toBe(true);
  });

  it('URL-encodes the automation id', async () => {
    mockAuthedFetch.mockResolvedValueOnce(new Response('{"ok":true}', { status: 200 }));
    const { result } = renderHook(() => useTestAutomation(), { wrapper });
    result.current.mutate({ automationId: 'rule with spaces', identifier: 'ENG-1' });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/automations/rule%20with%20spaces/test',
      expect.anything(),
    );
  });

  it('emits an error toast when the server returns non-ok', async () => {
    mockAuthedFetch.mockResolvedValueOnce(new Response('rule not found', { status: 500 }));
    const { result } = renderHook(() => useTestAutomation(), { wrapper });
    result.current.mutate({ automationId: 'gone', identifier: 'ENG-1' });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    const toasts = useToastStore.getState().toasts;
    expect(toasts.some((t) => t.message.toLowerCase().includes('failed'))).toBe(true);
    expect(toasts.some((t) => t.message.includes('rule not found'))).toBe(true);
  });
});
