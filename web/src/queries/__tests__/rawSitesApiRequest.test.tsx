import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, act, waitFor, render, screen, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import React from 'react';
import { useAnalyzeDeps, useSetDepsOverride } from '../deps';
import { useRetryOutboxEntry, useDiscardOutboxEntry } from '../outbox';
import { useTestAutomation } from '../automations';
import { useSkillsFix } from '../skills';
import { ModelsCard } from '../../pages/Settings/ModelsCard';
import { useToastStore } from '../../store/toastStore';
import { useItervoxStore } from '../../store/itervoxStore';

// BH-M2-1 — the remaining raw authedFetch + !res.ok sites now go through
// apiRequest: the server's {code,message} reaches the toast (never the raw
// JSON envelope) and an admission-rejected 503 is retried once.

type FetchFn = (input: string, init?: RequestInit) => Promise<Response>;
let fetchMock: ReturnType<typeof vi.fn<FetchFn>>;

function envelope(status: number, code: string, message: string, headers?: HeadersInit): Response {
  return new Response(JSON.stringify({ error: { code, message } }), { status, headers });
}

function wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return React.createElement(QueryClientProvider, { client: qc }, children);
}

function toastMessages(): string[] {
  return useToastStore.getState().toasts.map((t) => t.message);
}

beforeEach(() => {
  useToastStore.setState({ toasts: [], _timers: new Map() });
  useItervoxStore.setState({ refreshSnapshot: vi.fn().mockResolvedValue(undefined) });
  fetchMock = vi.fn<FetchFn>();
  global.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('useSetDepsOverride via apiRequest', () => {
  it('retries a 503 deps_override_queue_full once, then succeeds', async () => {
    fetchMock
      .mockResolvedValueOnce(
        envelope(503, 'deps_override_queue_full', 'queue full', { 'Retry-After': '0' }),
      )
      .mockResolvedValueOnce(new Response('{"queued":true}', { status: 202 }));
    const { result } = renderHook(() => useSetDepsOverride(), { wrapper });
    act(() => {
      result.current.mutate({ identifier: 'ENG-1', enabled: true });
    });
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(toastMessages()).toEqual(['Dismissed inferred blockers for ENG-1.']);
  });

  it('shows the server message on a 400, not the JSON envelope', async () => {
    fetchMock.mockResolvedValue(envelope(400, 'bad_request', 'identifier is not gated'));
    const { result } = renderHook(() => useSetDepsOverride(), { wrapper });
    act(() => {
      result.current.mutate({ identifier: 'ENG-1', enabled: false });
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(toastMessages()).toEqual(['Dependency override failed: identifier is not gated']);
  });
});

describe('server messages instead of raw JSON', () => {
  it('useAnalyzeDeps', async () => {
    fetchMock.mockResolvedValue(envelope(503, 'deps_analyzer_unavailable', 'no analyzer profile'));
    const { result } = renderHook(() => useAnalyzeDeps(), { wrapper });
    act(() => {
      result.current.mutate({});
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(toastMessages()).toEqual(['Analyze dependencies failed: no analyzer profile']);
  });

  it('useRetryOutboxEntry / useDiscardOutboxEntry', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(envelope(404, 'not_found', 'no outbox entry e-1')),
    );
    const retry = renderHook(() => useRetryOutboxEntry(), { wrapper });
    const discard = renderHook(() => useDiscardOutboxEntry(), { wrapper });
    act(() => {
      retry.result.current.mutate('e-1');
      discard.result.current.mutate('e-1');
    });
    await waitFor(() => {
      expect(retry.result.current.isError && discard.result.current.isError).toBe(true);
    });
    expect(toastMessages().sort()).toEqual([
      'Outbox discard failed: no outbox entry e-1',
      'Outbox retry failed: no outbox entry e-1',
    ]);
  });

  it('useTestAutomation', async () => {
    fetchMock.mockResolvedValue(envelope(404, 'not_found', 'automation qa not found'));
    const { result } = renderHook(() => useTestAutomation(), { wrapper });
    act(() => {
      result.current.mutate({ automationId: 'qa', identifier: 'ENG-1' });
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(toastMessages()).toEqual(['Automation test fire failed: automation qa not found']);
  });

  it('useSkillsFix', async () => {
    fetchMock.mockResolvedValue(envelope(400, 'bad_request', 'unsafe fix rejected'));
    const { result } = renderHook(() => useSkillsFix(), { wrapper });
    act(() => {
      result.current.mutate({ issueID: 'X', fix: {} } as never);
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(toastMessages()).toEqual(['Fix failed: unsafe fix rejected']);
  });

  it('ModelsCard refresh', async () => {
    fetchMock.mockResolvedValue(envelope(500, 'refresh_failed', 'codex CLI not found'));
    render(<ModelsCard availableModels={{ claude: [] }} />);
    fireEvent.click(screen.getAllByRole('button', { name: /refresh/i })[0]);
    await waitFor(() => {
      expect(toastMessages()).toEqual(['Refresh failed: codex CLI not found']);
    });
  });
});
