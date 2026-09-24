import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  ISSUE_KEY,
  ISSUES_KEY,
  useCancelIssue,
  useClearAllWorkspaces,
  useClearIssueLogs,
  usePostIssueComment,
  useProvideInput,
  useTriggerAIReview,
  useUpdateIssueState,
} from '../issues';
import { logIdentifiersKey, logsKey } from '../logs';
import type { TrackerIssue, StateSnapshot } from '../../types/schemas';
import { useItervoxStore } from '../../store/itervoxStore';
import { useToastStore } from '../../store/toastStore';

// ─── Helpers ──────────────────────────────────────────────────────────────────

function makeIssue(identifier: string, state = 'Todo'): TrackerIssue {
  return {
    identifier,
    title: `Title ${identifier}`,
    state,
    description: '',
    url: '',
    orchestratorState: 'idle',
    turnCount: 0,
    tokens: 0,
    elapsedMs: 0,
    lastMessage: '',
    error: '',
  };
}

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client: qc }, children);
  };
}

function freshClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
}

// ─── Setup ────────────────────────────────────────────────────────────────────

beforeEach(() => {
  // Reset real stores to initial state before each test
  useItervoxStore.setState({
    snapshot: null,
    logs: [],
    sseConnected: false,
    selectedIdentifier: null,
    tokenSamples: [],
  });
  useToastStore.setState({ toasts: [], _timers: new Map() });
});

afterEach(() => {
  vi.restoreAllMocks();
});

// ─── useUpdateIssueState ──────────────────────────────────────────────────────

describe('useUpdateIssueState', () => {
  it('applies optimistic update to the query cache immediately', async () => {
    const qc = freshClient();
    qc.setQueryData(ISSUES_KEY, [makeIssue('ABC-1', 'Todo')]);
    qc.setQueryData(ISSUE_KEY('ABC-1'), makeIssue('ABC-1', 'Todo'));

    // Mutation fn hangs so we can inspect optimistic state before it resolves
    global.fetch = vi.fn().mockReturnValue(new Promise<Response>(() => {}));

    const { result } = renderHook(() => useUpdateIssueState(), {
      wrapper: createWrapper(qc),
    });

    act(() => {
      result.current.mutate({ identifier: 'ABC-1', state: 'In Progress' });
    });

    // Wait one tick for onMutate's async steps (cancelQueries) to complete
    await new Promise((r) => setTimeout(r, 0));

    const cached = qc.getQueryData<TrackerIssue[]>(ISSUES_KEY);
    expect(cached?.[0].state).toBe('In Progress');
    expect(qc.getQueryData<TrackerIssue>(ISSUE_KEY('ABC-1'))?.state).toBe('In Progress');
  });

  it('rolls back the cache when the API call fails', async () => {
    const qc = freshClient();
    qc.setQueryData(ISSUES_KEY, [makeIssue('ABC-1', 'Todo')]);

    global.fetch = vi.fn().mockRejectedValue(new Error('network error'));

    const { result } = renderHook(() => useUpdateIssueState(), {
      wrapper: createWrapper(qc),
    });

    act(() => {
      result.current.mutate({ identifier: 'ABC-1', state: 'In Progress' });
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });

    const cached = qc.getQueryData<TrackerIssue[]>(ISSUES_KEY);
    expect(cached?.[0].state).toBe('Todo');
  });

  it('adds a toast when the API call fails', async () => {
    const qc = freshClient();
    qc.setQueryData(ISSUES_KEY, [makeIssue('ABC-1', 'Todo')]);

    global.fetch = vi.fn().mockRejectedValue(new Error('server exploded'));

    const { result } = renderHook(() => useUpdateIssueState(), {
      wrapper: createWrapper(qc),
    });

    act(() => {
      result.current.mutate({ identifier: 'ABC-1', state: 'Done' });
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });

    const toasts = useToastStore.getState().toasts;
    expect(toasts.length).toBeGreaterThan(0);
    expect(toasts[0].message).toMatch(/server exploded/i);
  });
});

describe('mutation refresh behavior', () => {
  it('refreshes snapshot and invalidates issue caches after provideInput succeeds', async () => {
    const qc = freshClient();
    const refreshSpy = vi
      .spyOn(useItervoxStore.getState(), 'refreshSnapshot')
      .mockResolvedValue(undefined);
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries');
    global.fetch = vi.fn().mockResolvedValue({ ok: true });

    const { result } = renderHook(() => useProvideInput(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync({ identifier: 'ABC-9', message: 'continue' });
    });

    expect(refreshSpy).toHaveBeenCalled();
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ISSUES_KEY });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ISSUE_KEY('ABC-9') });
  });

  it('refreshes snapshot and invalidates issue caches after AI review trigger succeeds', async () => {
    const qc = freshClient();
    const refreshSpy = vi
      .spyOn(useItervoxStore.getState(), 'refreshSnapshot')
      .mockResolvedValue(undefined);
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries');
    global.fetch = vi.fn().mockResolvedValue({ ok: true });

    const { result } = renderHook(() => useTriggerAIReview(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync('ABC-11');
    });

    expect(refreshSpy).toHaveBeenCalled();
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ISSUES_KEY });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ISSUE_KEY('ABC-11') });
  });

  it('invalidates log queries after clearing issue logs', async () => {
    const qc = freshClient();
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries');
    global.fetch = vi.fn().mockResolvedValue({ ok: true });

    const { result } = renderHook(() => useClearIssueLogs(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync('ABC-10');
    });

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: logsKey('ABC-10') });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: logIdentifiersKey() });
  });

  it('refreshes snapshot and invalidates global log queries after clearing all workspaces', async () => {
    const qc = freshClient();
    const refreshSpy = vi
      .spyOn(useItervoxStore.getState(), 'refreshSnapshot')
      .mockResolvedValue(undefined);
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries');
    global.fetch = vi.fn().mockResolvedValue({ ok: true });

    const { result } = renderHook(() => useClearAllWorkspaces(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync();
    });

    expect(refreshSpy).toHaveBeenCalled();
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ['logs'] });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ['sublogs'] });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: logIdentifiersKey() });
  });
});

// ─── useProvideInput — 409 inline_input_enabled ────────────────────────────────

describe('useProvideInput inline-input conflict', () => {
  it('shows the inline-input message, not the raw status string, on 409', async () => {
    const qc = freshClient();
    global.fetch = vi.fn().mockResolvedValue({ ok: false, status: 409 });

    const { result } = renderHook(() => useProvideInput(), {
      wrapper: createWrapper(qc),
    });

    act(() => {
      result.current.mutate({ identifier: 'ABC-9', message: 'continue' });
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });

    const toasts = useToastStore.getState().toasts;
    expect(toasts.length).toBeGreaterThan(0);
    expect(toasts[0].message).not.toMatch(/provideInput failed: 409/);
    expect(toasts[0].message).toMatch(/inline input is on/i);
  });

  it('refreshes the snapshot on 409 so the panel flips to the inline notice', async () => {
    const qc = freshClient();
    const refreshSpy = vi
      .spyOn(useItervoxStore.getState(), 'refreshSnapshot')
      .mockResolvedValue(undefined);
    global.fetch = vi.fn().mockResolvedValue({ ok: false, status: 409 });

    const { result } = renderHook(() => useProvideInput(), {
      wrapper: createWrapper(qc),
    });

    act(() => {
      result.current.mutate({ identifier: 'ABC-9', message: 'continue' });
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });

    expect(refreshSpy).toHaveBeenCalled();
  });

  it('still produces the generic failure behavior on a 500', async () => {
    const qc = freshClient();
    const refreshSpy = vi
      .spyOn(useItervoxStore.getState(), 'refreshSnapshot')
      .mockResolvedValue(undefined);
    global.fetch = vi.fn().mockResolvedValue({ ok: false, status: 500 });

    const { result } = renderHook(() => useProvideInput(), {
      wrapper: createWrapper(qc),
    });

    act(() => {
      result.current.mutate({ identifier: 'ABC-9', message: 'continue' });
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });

    const toasts = useToastStore.getState().toasts;
    expect(toasts.length).toBeGreaterThan(0);
    // Unchanged from before this fix: non-409 failures still surface the
    // generic thrown-error message, not the inline-input notice.
    expect(toasts[0].message).toMatch(/provideInput failed: 500/);
    // refreshSnapshot is only called by the 409 conflict path, not on a
    // generic failure — the reply box does not need to disappear here.
    expect(refreshSpy).not.toHaveBeenCalled();
  });
});

// ─── usePostIssueComment ────────────────────────────────────────────────────

describe('usePostIssueComment', () => {
  it('toasts "queued" on 202 and invalidates the issue caches', async () => {
    const qc = freshClient();
    const refreshSpy = vi
      .spyOn(useItervoxStore.getState(), 'refreshSnapshot')
      .mockResolvedValue(undefined);
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries');
    global.fetch = vi
      .fn()
      .mockResolvedValue({ ok: true, status: 202, json: () => Promise.resolve({ queued: true }) });

    const { result } = renderHook(() => usePostIssueComment(), { wrapper: createWrapper(qc) });
    await act(async () => {
      await result.current.mutateAsync({ identifier: 'ABC-9', body: 'nice' });
    });

    const toasts = useToastStore.getState().toasts;
    expect(toasts.at(-1)?.message).toMatch(/queued/i);
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ISSUE_KEY('ABC-9') });
    expect(refreshSpy).toHaveBeenCalled();
  });

  it('toasts "posted" on 200', async () => {
    const qc = freshClient();
    const refreshSpy = vi
      .spyOn(useItervoxStore.getState(), 'refreshSnapshot')
      .mockResolvedValue(undefined);
    global.fetch = vi
      .fn()
      .mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve({ ok: true }) });
    const { result } = renderHook(() => usePostIssueComment(), { wrapper: createWrapper(qc) });
    await act(async () => {
      await result.current.mutateAsync({ identifier: 'ABC-9', body: 'nice' });
    });
    expect(useToastStore.getState().toasts.at(-1)?.message).toMatch(/posted/i);
    expect(refreshSpy).toHaveBeenCalled();
  });

  it('surfaces the server error message on failure', async () => {
    const qc = freshClient();
    global.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 404,
      json: () => Promise.resolve({ error: { code: 'not_found', message: 'issue not found' } }),
    });
    const { result } = renderHook(() => usePostIssueComment(), { wrapper: createWrapper(qc) });
    await act(async () => {
      await expect(
        result.current.mutateAsync({ identifier: 'NOPE-1', body: 'x' }),
      ).rejects.toThrow();
    });
    expect(useToastStore.getState().toasts.at(-1)?.message).toMatch(/issue not found/i);
  });
});

// ─── useCancelIssue ───────────────────────────────────────────────────────────

describe('useCancelIssue', () => {
  it('applies optimistic patch to the snapshot on mutate', async () => {
    const qc = freshClient();
    qc.setQueryData(ISSUES_KEY, [makeIssue('ABC-2', 'In Progress')]);
    qc.setQueryData(ISSUE_KEY('ABC-2'), makeIssue('ABC-2', 'In Progress'));

    const baseSnapshot = {
      generatedAt: new Date().toISOString(),
      orchestratorState: 'running',
      running: [
        {
          identifier: 'ABC-2',
          state: 'In Progress',
          turnCount: 1,
          tokens: 100,
          inputTokens: 50,
          outputTokens: 50,
          elapsedMs: 1000,
          startedAt: new Date().toISOString(),
          sessionId: 's1',
        },
      ],
      retrying: [],
      paused: [],
      pausedWithPR: [],
      counts: { running: 1, retrying: 0, paused: 0 },
      maxConcurrentAgents: 5,
      rateLimits: null,
    } as unknown as StateSnapshot;

    useItervoxStore.setState({ snapshot: baseSnapshot });

    const patchSpy = vi.spyOn(useItervoxStore.getState(), 'patchSnapshot');

    global.fetch = vi.fn().mockReturnValue(new Promise<Response>(() => {}));

    const { result } = renderHook(() => useCancelIssue(), {
      wrapper: createWrapper(qc),
    });

    act(() => {
      result.current.mutate('ABC-2');
    });

    await new Promise((r) => setTimeout(r, 0));

    expect(patchSpy).toHaveBeenCalled();
    const patchArg = patchSpy.mock.calls[0][0];
    expect(patchArg.paused).toContain('ABC-2');
    expect(qc.getQueryData<TrackerIssue>(ISSUE_KEY('ABC-2'))?.orchestratorState).toBe('paused');
  });

  it('rolls back both cache and snapshot on API error', async () => {
    const qc = freshClient();
    qc.setQueryData(ISSUES_KEY, [makeIssue('ABC-2', 'In Progress')]);

    const baseSnapshot = {
      generatedAt: new Date().toISOString(),
      orchestratorState: 'running',
      running: [
        {
          identifier: 'ABC-2',
          state: 'In Progress',
          turnCount: 0,
          tokens: 0,
          inputTokens: 0,
          outputTokens: 0,
          elapsedMs: 0,
          startedAt: new Date().toISOString(),
          sessionId: 's1',
        },
      ],
      retrying: [],
      paused: [],
      pausedWithPR: [],
      counts: { running: 1, retrying: 0, paused: 0 },
      maxConcurrentAgents: 5,
      rateLimits: null,
    } as unknown as StateSnapshot;

    useItervoxStore.setState({ snapshot: baseSnapshot });

    global.fetch = vi.fn().mockRejectedValue(new Error('cancel failed'));

    const { result } = renderHook(() => useCancelIssue(), {
      wrapper: createWrapper(qc),
    });

    act(() => {
      result.current.mutate('ABC-2');
    });

    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });

    // Cache should be restored
    const cached = qc.getQueryData<TrackerIssue[]>(ISSUES_KEY);
    expect(cached?.[0].orchestratorState).toBe('idle');
    expect(useItervoxStore.getState().snapshot).toEqual(baseSnapshot);

    // A toast should have been shown
    const toasts = useToastStore.getState().toasts;
    expect(toasts.length).toBeGreaterThan(0);
  });
});
