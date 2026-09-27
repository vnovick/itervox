import { act, renderHook, waitFor } from '@testing-library/react';
import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useCancelIssue, useResumeIssue, useTerminateIssue } from '../issues';
import { useItervoxStore } from '../../store/itervoxStore';
import { useToastStore } from '../../store/toastStore';

// CORE-073 — issue control mutations confirm success with a toast worded as
// "requested": the daemon only enqueues the action, so the toast must not
// claim it already happened. A failed mutation shows only the error toast.

function wrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client: qc }, children);
  };
}

const freshClient = () =>
  new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });

const ok = () => vi.fn().mockResolvedValue(new Response('{}', { status: 200 }));
const fail = () =>
  vi
    .fn()
    .mockResolvedValue(
      new Response(JSON.stringify({ error: { code: 'x', message: 'nope' } }), { status: 500 }),
    );

let addToast: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  useItervoxStore.setState({ snapshot: null });
  useToastStore.setState({ toasts: [], _timers: new Map() });
  vi.spyOn(useItervoxStore.getState(), 'refreshSnapshot').mockResolvedValue(undefined);
  addToast = vi.spyOn(useToastStore.getState(), 'addToast');
});

afterEach(() => {
  vi.restoreAllMocks();
});

type Hook = () => { mutate: (id: string) => void; isSuccess: boolean; isError: boolean };

const cases: Array<[string, Hook, string]> = [
  ['pause', () => useCancelIssue(), 'Pause requested for ENG-1'],
  [
    'cancel retry',
    () => useCancelIssue({ verb: 'Cancel retry' }),
    'Cancel retry requested for ENG-1',
  ],
  ['resume', () => useResumeIssue(), 'Resume requested for ENG-1'],
  ['discard (terminate a paused issue)', () => useTerminateIssue(), 'Discard requested for ENG-1'],
];

describe('issues mutations', () => {
  it('pause, resume, terminate and discard toast success', async () => {
    for (const [, hook, message] of cases) {
      addToast.mockClear();
      global.fetch = ok();
      const { result } = renderHook(hook, { wrapper: wrapper(freshClient()) });
      act(() => {
        result.current.mutate('ENG-1');
      });
      await waitFor(() => {
        expect(result.current.isSuccess).toBe(true);
      });
      expect(addToast).toHaveBeenCalledTimes(1);
      expect(addToast).toHaveBeenCalledWith(message, 'success');
    }

    // Rollback path: a failed mutation shows the error, never a success toast.
    for (const [, hook] of cases) {
      addToast.mockClear();
      global.fetch = fail();
      const { result } = renderHook(hook, { wrapper: wrapper(freshClient()) });
      act(() => {
        result.current.mutate('ENG-1');
      });
      await waitFor(() => {
        expect(result.current.isError).toBe(true);
      });
      expect(addToast).toHaveBeenCalledTimes(1);
      expect(addToast.mock.calls[0][1]).not.toBe('success');
    }
  });
});
