import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  ApiError,
  ISSUE_CONTROL_RETRY_MAX_MS,
  retryAfterDelayMs,
  useCancelIssue,
  useDismissInput,
  useProvideInput,
  useReanalyzeIssue,
  useResumeIssue,
  useTerminateIssue,
} from '../issues';
import { useItervoxStore } from '../../store/itervoxStore';
import { useToastStore } from '../../store/toastStore';

// CORE-005 (web half) — issue-control endpoints answer 503 + Retry-After when
// the orchestrator's event channel is full. Nothing was enqueued in that
// case, so the client retries exactly once, only on a 503, honouring
// Retry-After. Every other status and every transport failure is surfaced
// immediately.

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client: qc }, children);
  };
}

function freshClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
}

function busy(retryAfter: string | null = '0'): Response {
  const headers = new Headers();
  if (retryAfter !== null) headers.set('Retry-After', retryAfter);
  return new Response(
    JSON.stringify({ error: { code: 'orchestrator_busy', message: 'queue full' } }),
    { status: 503, headers },
  );
}

function ok(): Response {
  return new Response(JSON.stringify({ ok: true }), { status: 200 });
}

function status(code: number): Response {
  return new Response(JSON.stringify({ error: { code: 'x', message: 'x' } }), { status: code });
}

// Each issue-control mutation whose endpoint can return 503 (handlers.go
// writeBusyOr404: resume, cancel, terminate, reanalyze, provide-input,
// dismiss-input), with the call it makes and its fallback error prefix.
const CONTROLS = [
  {
    name: 'useCancelIssue',
    hook: useCancelIssue,
    vars: 'ABC-1' as unknown,
    path: '/api/v1/issues/ABC-1/cancel',
    op: 'cancelIssue',
  },
  {
    name: 'useResumeIssue',
    hook: useResumeIssue,
    vars: 'ABC-1' as unknown,
    path: '/api/v1/issues/ABC-1/resume',
    op: 'resumeIssue',
  },
  {
    name: 'useTerminateIssue',
    hook: useTerminateIssue,
    vars: 'ABC-1' as unknown,
    path: '/api/v1/issues/ABC-1/terminate',
    op: 'terminateIssue',
  },
  {
    name: 'useReanalyzeIssue',
    hook: useReanalyzeIssue,
    vars: 'ABC-1' as unknown,
    path: '/api/v1/issues/ABC-1/reanalyze',
    op: 'reanalyzeIssue',
  },
  {
    name: 'useProvideInput',
    hook: useProvideInput,
    vars: { identifier: 'ABC-1', message: 'continue' } as unknown,
    path: '/api/v1/issues/ABC-1/provide-input',
    op: 'provideInput',
  },
  {
    name: 'useDismissInput',
    hook: useDismissInput,
    vars: 'ABC-1' as unknown,
    path: '/api/v1/issues/ABC-1/dismiss-input',
    op: 'dismissInput',
  },
] as const;

type AnyMutationHook = () => {
  mutate: (vars: unknown) => void;
  isError: boolean;
  isSuccess: boolean;
  error: unknown;
};

function run(control: (typeof CONTROLS)[number]) {
  const qc = freshClient();
  const { result } = renderHook(() => (control.hook as unknown as AnyMutationHook)(), {
    wrapper: createWrapper(qc),
  });
  act(() => {
    result.current.mutate(control.vars);
  });
  return result;
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  useItervoxStore.setState({ snapshot: null });
  vi.spyOn(useItervoxStore.getState(), 'refreshSnapshot').mockResolvedValue(undefined);
  useToastStore.setState({ toasts: [], _timers: new Map() });
  fetchMock = vi.fn();
  global.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe.each(CONTROLS)('$name — single retry on 503', (control) => {
  it('503 then 200 succeeds with exactly two calls to the same endpoint', async () => {
    fetchMock.mockResolvedValueOnce(busy('0')).mockResolvedValueOnce(ok());
    const result = run(control);
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[0][0]).toBe(control.path);
    expect(fetchMock.mock.calls[1][0]).toBe(control.path);
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe('POST');
  });

  // M0-close re-check G7: CORE-005 requires these mutations to reuse
  // apiErrorFromResponse, so the second 503's server envelope
  // ({code:'orchestrator_busy', message:'queue full'}) is what surfaces —
  // this row deliberately changed from expecting the generic
  // `${op} failed: 503` string.
  it('503 then 503 fails after two calls and surfaces the server error envelope', async () => {
    fetchMock.mockResolvedValueOnce(busy('0')).mockResolvedValueOnce(busy('0'));
    const result = run(control);
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    const err = result.current.error as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.message).toBe('queue full');
    expect(err.code).toBe('orchestrator_busy');
    expect(err.status).toBe(503);
    const toasts = useToastStore.getState().toasts;
    expect(toasts.length).toBeGreaterThan(0);
    expect(toasts[0].message).toBe('queue full');
  });

  it('a non-retried error carries the server {code,message}', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ error: { code: 'not_found', message: 'no such issue' } }), {
        status: 404,
      }),
    );
    const result = run(control);
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const err = result.current.error as ApiError;
    expect(err.message).toBe('no such issue');
    expect(err.code).toBe('not_found');
    expect(err.status).toBe(404);
  });

  it('falls back to the generic message when the body is not an error envelope', async () => {
    fetchMock.mockResolvedValue(new Response('<html>bad gateway</html>', { status: 502 }));
    const result = run(control);
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect((result.current.error as Error).message).toBe(`${control.op} failed: 502`);
  });

  it.each([404, 409, 500])('does not retry a %i', async (code) => {
    fetchMock.mockResolvedValue(status(code));
    const result = run(control);
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('does not retry a network error', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
    const result = run(control);
    await waitFor(() => {
      expect(result.current.isError).toBe(true);
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect((result.current.error as Error).message).toBe('Failed to fetch');
  });
});

describe('provideInput retry keeps the request body', () => {
  it('re-sends the same JSON body on the retry', async () => {
    fetchMock.mockResolvedValueOnce(busy('0')).mockResolvedValueOnce(ok());
    const result = run(CONTROLS[4]);
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    const first = fetchMock.mock.calls[0][1] as RequestInit;
    const second = fetchMock.mock.calls[1][1] as RequestInit;
    expect(second.body).toBe(first.body);
    expect(second.body).toBe(JSON.stringify({ message: 'continue' }));
  });
});

describe('Retry-After is honoured', () => {
  it('waits the advertised seconds before the single retry', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: false, toFake: ['setTimeout', 'clearTimeout'] });
    fetchMock.mockResolvedValueOnce(busy('2')).mockResolvedValueOnce(ok());
    const result = run(CONTROLS[0]);

    await vi.advanceTimersByTimeAsync(1_900);
    expect(fetchMock).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(200);
    expect(fetchMock).toHaveBeenCalledTimes(2);

    vi.useRealTimers();
    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
  });

  it('caps an absurd Retry-After instead of hanging the mutation', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: false, toFake: ['setTimeout', 'clearTimeout'] });
    fetchMock.mockResolvedValueOnce(busy('3600')).mockResolvedValueOnce(ok());
    run(CONTROLS[0]);

    await vi.advanceTimersByTimeAsync(5_000);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});

describe('retryAfterDelayMs', () => {
  const now = Date.parse('2026-09-25T12:00:00Z');
  it.each([
    ['1', 1_000],
    ['0', 0],
    [' 2 ', 2_000],
    ['3600', ISSUE_CONTROL_RETRY_MAX_MS],
    [null, 1_000],
    ['', 1_000],
    ['soon', 1_000],
    ['-1', 1_000],
    ['Fri, 25 Sep 2026 12:00:03 GMT', 3_000],
    ['Fri, 25 Sep 2026 11:59:00 GMT', 0],
    ['Fri, 25 Sep 2026 13:00:00 GMT', ISSUE_CONTROL_RETRY_MAX_MS],
  ])('%j -> %i ms', (header, expected) => {
    expect(retryAfterDelayMs(header, now)).toBe(expected);
  });
});
