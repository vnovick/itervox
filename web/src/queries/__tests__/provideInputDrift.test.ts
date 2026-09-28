import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useProvideInput } from '../issues';
import { useItervoxStore } from '../../store/itervoxStore';
import { useToastStore } from '../../store/toastStore';

// CORE-047 — useProvideInput's 409 inline_input_enabled recovery calls
// refreshSnapshot(). Before the fix, a snapshot that failed the schema on
// that refresh was swallowed (DEV-only warn), so the stale reply box stayed
// on screen with no signal. The failure must now land in schemaDriftCount.

type FetchFn = (input: string, init?: RequestInit) => Promise<Response>;
let fetchMock: ReturnType<typeof vi.fn<FetchFn>>;

beforeEach(() => {
  vi.stubEnv('DEV', false);
  useItervoxStore.setState({ snapshot: null, schemaDriftCount: 0, lastSchemaDrift: null });
  useToastStore.setState({ toasts: [], _timers: new Map() });
  fetchMock = vi.fn<FetchFn>();
  global.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe('useProvideInput inline-input conflict recovery', () => {
  it('records schema drift when the recovery refresh cannot parse the snapshot', async () => {
    fetchMock.mockImplementation((url: string) => {
      if (url === '/api/v1/state') {
        return Promise.resolve(new Response(JSON.stringify({ counts: 'nope' }), { status: 200 }));
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({ error: { code: 'inline_input_enabled', message: 'inline input on' } }),
          { status: 409 },
        ),
      );
    });
    const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const { result } = renderHook(() => useProvideInput(), {
      wrapper: ({ children }: { children: React.ReactNode }) =>
        React.createElement(QueryClientProvider, { client: qc }, children),
    });
    act(() => {
      result.current.mutate({ identifier: 'A-1', message: 'hi' });
    });
    await waitFor(() => {
      expect(useItervoxStore.getState().schemaDriftCount).toBe(1);
    });
    expect(useItervoxStore.getState().lastSchemaDrift?.reason).toBe('schema');
  });
});
