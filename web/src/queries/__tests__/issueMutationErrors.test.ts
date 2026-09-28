import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  InlineInputEnabledError,
  useClearAllLogs,
  useClearAllWorkspaces,
  useClearIssueLogs,
  useClearIssueSubLogs,
  useProvideInput,
  useSetIssueBackend,
  useSetIssueProfile,
  useTriggerAIReview,
  useUpdateIssueState,
} from '../issues';
import { ApiError } from '../../auth/apiRequest';
import { useItervoxStore } from '../../store/itervoxStore';
import { useToastStore } from '../../store/toastStore';

// CORE-049 — every issue mutation surfaces the server's {code,message} in its
// toast instead of "<op> failed: <status>".

function wrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client: qc }, children);
  };
}

function serverError(status: number, code: string, message: string): Response {
  return new Response(JSON.stringify({ error: { code, message } }), { status });
}

type FetchFn = (input: string, init?: RequestInit) => Promise<Response>;
let fetchMock: ReturnType<typeof vi.fn<FetchFn>>;

beforeEach(() => {
  useItervoxStore.setState({ snapshot: null });
  useToastStore.setState({ toasts: [], _timers: new Map() });
  fetchMock = vi.fn<FetchFn>();
  global.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  vi.restoreAllMocks();
});

type AnyHook = () => { mutate: (v: unknown) => void; isError: boolean; error: unknown };

const MUTATIONS: { name: string; hook: AnyHook; vars: unknown }[] = [
  {
    name: 'useUpdateIssueState',
    hook: useUpdateIssueState as unknown as AnyHook,
    vars: { identifier: 'A-1', state: 'Done' },
  },
  {
    name: 'useSetIssueProfile',
    hook: useSetIssueProfile as unknown as AnyHook,
    vars: { identifier: 'A-1', profile: 'p' },
  },
  {
    name: 'useSetIssueBackend',
    hook: useSetIssueBackend as unknown as AnyHook,
    vars: { identifier: 'A-1', backend: 'gemini' },
  },
  { name: 'useTriggerAIReview', hook: useTriggerAIReview as unknown as AnyHook, vars: 'A-1' },
  { name: 'useClearIssueLogs', hook: useClearIssueLogs as unknown as AnyHook, vars: 'A-1' },
  { name: 'useClearIssueSubLogs', hook: useClearIssueSubLogs as unknown as AnyHook, vars: 'A-1' },
  { name: 'useClearAllLogs', hook: useClearAllLogs as unknown as AnyHook, vars: undefined },
  {
    name: 'useClearAllWorkspaces',
    hook: useClearAllWorkspaces as unknown as AnyHook,
    vars: undefined,
  },
];

describe.each(MUTATIONS)('$name', ({ hook, vars }) => {
  it('toasts the server {code,message}', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(serverError(400, 'bad_request', 'the server explains why')),
    );
    const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => hook(), { wrapper: wrapper(qc) });
    act(() => {
      result.current.mutate(vars);
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    const err = result.current.error as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.code).toBe('bad_request');
    expect(useToastStore.getState().toasts[0]?.message).toBe('the server explains why');
  });
});

describe('useProvideInput conflict codes', () => {
  it('useProvideInput 409 with a non-inline code toasts the server message', async () => {
    const refreshSpy = vi
      .spyOn(useItervoxStore.getState(), 'refreshSnapshot')
      .mockResolvedValue(undefined);
    fetchMock.mockResolvedValue(serverError(409, 'other', 'a different conflict'));
    const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => useProvideInput(), { wrapper: wrapper(qc) });
    act(() => {
      result.current.mutate({ identifier: 'A-1', message: 'hi' });
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(result.current.error).not.toBeInstanceOf(InlineInputEnabledError);
    expect(useToastStore.getState().toasts[0]?.message).toBe('a different conflict');
    expect(refreshSpy).not.toHaveBeenCalled();
  });

  it('maps 409 inline_input_enabled to InlineInputEnabledError, an ApiError', async () => {
    vi.spyOn(useItervoxStore.getState(), 'refreshSnapshot').mockResolvedValue(undefined);
    fetchMock.mockResolvedValue(serverError(409, 'inline_input_enabled', 'inline input is on'));
    const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => useProvideInput(), { wrapper: wrapper(qc) });
    act(() => {
      result.current.mutate({ identifier: 'A-1', message: 'hi' });
    });
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    const err = result.current.error as InlineInputEnabledError;
    expect(err).toBeInstanceOf(InlineInputEnabledError);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.code).toBe('inline_input_enabled');
    expect(err.status).toBe(409);
  });
});
