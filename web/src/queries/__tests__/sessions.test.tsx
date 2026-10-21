import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import { sessionKey, useSendSessionMessage, useSessions } from '../sessions';

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
  useToastStore.setState({ toasts: [] });
});

describe('useSessions', () => {
  it('reads GET /api/v1/sessions through the authed transport and parses the rows', async () => {
    mockAuthedFetch.mockResolvedValue(
      new Response(
        JSON.stringify({
          generatedAt: '2026-10-15T09:00:00Z',
          sessions: [
            { id: 'sess-1', owner: 'itervox', identifier: 'ENG-1', status: 'input_required' },
          ],
        }),
        { status: 200 },
      ),
    );
    const { result } = renderHook(() => useSessions(), { wrapper });
    await waitFor(() => {
      expect(result.current.sessions).toHaveLength(1);
    });
    expect(result.current.sessions[0].status).toBe('input_required');
    expect(mockAuthedFetch).toHaveBeenCalledWith('/api/v1/sessions', expect.anything());
  });
});

describe('useSendSessionMessage', () => {
  it('POSTs the message to /api/v1/sessions/{id}/message', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ message: { id: 'm1', status: 'queued' } }), { status: 202 }),
    );
    const { result } = renderHook(() => useSendSessionMessage(), { wrapper });
    act(() => {
      result.current.mutate({ id: 'sess/1', message: 'hi' });
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/sessions/sess%2F1/message',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ message: 'hi' }) }),
    );
  });

  it('toasts a refusal', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({ error: { code: 'input_required', message: 'the session is waiting' } }),
        { status: 409 },
      ),
    );
    const { result } = renderHook(() => useSendSessionMessage(), { wrapper });
    act(() => {
      result.current.mutate({ id: 's', message: 'hi' });
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(useToastStore.getState().toasts[0]?.message).toContain('Message not sent');
  });
});

describe('sessionKey', () => {
  it('prefers the CLI session id and falls back to the run id', () => {
    expect(sessionKey({ id: 'sess-1', runId: 'run-1' })).toBe('sess-1');
    expect(sessionKey({ id: '', runId: 'run-1' })).toBe('run-1');
    expect(sessionKey({ id: '' })).toBe('');
  });
});
