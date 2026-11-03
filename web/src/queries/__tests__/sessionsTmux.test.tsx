import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import { useSendSessionMessage, useSessions } from '../sessions';
import {
  SCREEN_REFETCH_MS,
  useKillSession,
  useSendSessionKey,
  useSessionScreen,
  useStartSession,
} from '../sessionsTmux';

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

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

beforeEach(() => {
  mockAuthedFetch.mockReset();
  useToastStore.setState({ toasts: [] });
});

describe('tmux session queries (#178)', () => {
  it('polls GET /screen about once a second while enabled', async () => {
    mockAuthedFetch.mockImplementation(() =>
      Promise.resolve(
        json({
          id: 'h',
          pane: '%3',
          label: 'work:0.0',
          text: 'hello',
          lines: 200,
          capturedAt: 'x',
        }),
      ),
    );
    expect(SCREEN_REFETCH_MS).toBe(1000);
    const { result } = renderHook(() => useSessionScreen('h/1', true), { wrapper });
    await waitFor(() => {
      expect(result.current.data?.text).toBe('hello');
    });
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/sessions/h%2F1/screen?lines=200',
      expect.anything(),
    );
    await waitFor(
      () => {
        expect(mockAuthedFetch.mock.calls.length).toBeGreaterThanOrEqual(2);
      },
      { timeout: 3000 },
    );
  });

  it('does not poll a disabled screen', async () => {
    renderHook(() => useSessionScreen('h', false), { wrapper });
    await new Promise((r) => setTimeout(r, 50));
    expect(mockAuthedFetch).not.toHaveBeenCalled();
  });

  it('POSTs a key to /keys', async () => {
    mockAuthedFetch.mockResolvedValueOnce(json({ ok: true }, 202));
    const { result } = renderHook(() => useSendSessionKey(), { wrapper });
    act(() => {
      result.current.mutate({ id: 'h', key: 'Escape' });
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/sessions/h/keys',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ key: 'Escape' }) }),
    );
  });

  it('sends text with submit to /message', async () => {
    mockAuthedFetch.mockResolvedValueOnce(json({ message: { id: 'm' } }, 202));
    const { result } = renderHook(() => useSendSessionMessage(), { wrapper });
    act(() => {
      result.current.mutate({ id: 'h', message: 'go', submit: false });
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/sessions/h/message',
      expect.objectContaining({ body: JSON.stringify({ message: 'go', submit: false }) }),
    );
  });

  it('POSTs a new session and returns its row', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      json(
        {
          session: {
            id: 'itervox-abc123',
            owner: 'itervox',
            kind: 'tmux',
            identifier: '',
            status: 'running',
            tmuxPane: null,
            controllable: true,
            attachCommand: 'tmux attach -t itervox-abc123',
          },
        },
        201,
      ),
    );
    const { result } = renderHook(() => useStartSession(), { wrapper });
    act(() => {
      result.current.mutate({ backend: 'claude', prompt: 'hi' });
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(result.current.data?.attachCommand).toBe('tmux attach -t itervox-abc123');
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/sessions',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ backend: 'claude', prompt: 'hi' }),
      }),
    );
  });

  it('toasts tmux_unavailable with the daemon message', async () => {
    mockAuthedFetch.mockResolvedValueOnce(
      json(
        {
          error: {
            code: 'tmux_unavailable',
            message: "tmux is not installed on the daemon's host: brew install tmux",
          },
        },
        409,
      ),
    );
    const { result } = renderHook(() => useStartSession(), { wrapper });
    act(() => {
      result.current.mutate({ backend: 'claude' });
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(useToastStore.getState().toasts[0]?.message).toContain('brew install tmux');
  });

  it('DELETEs a session to kill it', async () => {
    mockAuthedFetch.mockResolvedValueOnce(json({ ok: true }));
    const { result } = renderHook(() => useKillSession(), { wrapper });
    act(() => {
      result.current.mutate('itervox-abc123');
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockAuthedFetch).toHaveBeenCalledWith(
      '/api/v1/sessions/itervox-abc123',
      expect.objectContaining({ method: 'DELETE' }),
    );
  });

  it('reads the tmux status and New session options from GET /api/v1/sessions', async () => {
    mockAuthedFetch.mockResolvedValue(
      json({
        generatedAt: 'x',
        sessions: [],
        tmux: { available: false, installHint: 'brew install tmux', control: false },
        newSession: { projectRoot: '/w', folders: ['/w'], profiles: [], max: 8 },
      }),
    );
    const { result } = renderHook(() => useSessions(), { wrapper });
    await waitFor(() => {
      expect(result.current.tmux).toEqual({
        available: false,
        installHint: 'brew install tmux',
        control: false,
      });
    });
    expect(result.current.newSession?.folders).toEqual(['/w']);
  });
});
