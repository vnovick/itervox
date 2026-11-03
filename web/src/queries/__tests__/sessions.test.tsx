import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import {
  sessionKey,
  sessionLabel,
  useSendSessionMessage,
  useSessionTranscript,
  useSessions,
} from '../sessions';

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

describe('useSessionTranscript (#178 step 3)', () => {
  it('reads the newest page, then older pages by cursor, oldest entries first', async () => {
    mockAuthedFetch.mockImplementation((url: string) => {
      const older = url.includes('before=4096');
      return Promise.resolve(
        new Response(
          JSON.stringify(
            older
              ? {
                  id: 'h',
                  entries: [{ role: 'user', kind: 'text', text: 'first' }],
                  hasMore: false,
                  size: 9000,
                }
              : {
                  id: 'h',
                  entries: [{ role: 'assistant', kind: 'text', text: 'latest' }],
                  before: 4096,
                  hasMore: true,
                  size: 9000,
                },
          ),
          { status: 200 },
        ),
      );
    });
    const { result } = renderHook(() => useSessionTranscript('hand/1'), { wrapper });
    await waitFor(() => {
      expect(result.current.entries).toHaveLength(1);
    });
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/sessions/hand%2F1/transcript?limit=100',
      expect.anything(),
    );
    expect(result.current.hasNextPage).toBe(true);
    await act(async () => {
      await result.current.fetchNextPage();
    });
    await waitFor(() => {
      expect(result.current.entries.map((e) => e.text)).toEqual(['first', 'latest']);
    });
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/sessions/hand%2F1/transcript?limit=100&before=4096',
      expect.anything(),
    );
    expect(result.current.hasNextPage).toBe(false);
  });
});

describe('sessionLabel', () => {
  it('names a session by its issue, title, directory or id', () => {
    expect(sessionLabel({ id: 'x', identifier: 'ENG-1', title: 't' })).toBe('ENG-1');
    expect(sessionLabel({ id: 'x', identifier: '', title: 'Add health check' })).toBe(
      'Add health check',
    );
    expect(sessionLabel({ id: 'x', identifier: '', cwd: '/work/app/' })).toBe('app');
    expect(sessionLabel({ id: '1234567890', identifier: '' })).toBe('12345678');
  });
});
