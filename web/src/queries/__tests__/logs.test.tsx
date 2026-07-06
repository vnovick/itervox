import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { LIVE_LOG_ENTRY_CAP, useIssueLogs, useSubagentLogs } from '../logs';

interface MockStreamHandle {
  url: string;
  onOpen?: () => void;
  onMessage: (msg: { event?: string; data: string; id?: string; retry?: number }) => void;
  onDisconnect?: () => void;
  close: ReturnType<typeof vi.fn>;
}

const streamHandles: MockStreamHandle[] = [];

vi.mock('../../auth/authedEventStream', () => ({
  openAuthedEventStream: (url: string, opts: Omit<MockStreamHandle, 'url' | 'close'>) => {
    const handle: MockStreamHandle = {
      url,
      onOpen: opts.onOpen,
      onMessage: opts.onMessage,
      onDisconnect: opts.onDisconnect,
      close: vi.fn(),
    };
    streamHandles.push(handle);
    return () => {
      handle.close();
    };
  },
}));

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client }, children);
  };
}

beforeEach(() => {
  streamHandles.length = 0;
  global.fetch = vi.fn();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('useSubagentLogs', () => {
  it('uses SSE for live sublogs and appends streamed entries', () => {
    const { result } = renderHook(() => useSubagentLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });

    expect(streamHandles).toHaveLength(1);
    expect(streamHandles[0].url).toBe('/api/v1/issues/ENG-1/sublog-stream');

    act(() => {
      streamHandles[0].onOpen?.();
      streamHandles[0].onMessage({
        event: 'sublog',
        data: JSON.stringify({
          level: 'INFO',
          event: 'text',
          message: 'hello',
          sessionId: 'sess-1',
        }),
      });
    });

    expect(result.current.isError).toBe(false);
    expect(result.current.data).toEqual([
      {
        level: 'INFO',
        event: 'text',
        message: 'hello',
        sessionId: 'sess-1',
      },
    ]);
    expect(global.fetch).not.toHaveBeenCalled();
  });

  it('caps live subagent log entries locally', () => {
    const { result } = renderHook(() => useSubagentLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });

    act(() => {
      streamHandles[0].onOpen?.();
      for (let i = 0; i < LIVE_LOG_ENTRY_CAP + 2; i += 1) {
        streamHandles[0].onMessage({
          event: 'sublog',
          data: JSON.stringify({
            level: 'INFO',
            event: 'text',
            message: `line-${String(i)}`,
          }),
        });
      }
    });

    expect(result.current.data).toHaveLength(LIVE_LOG_ENTRY_CAP);
    expect(result.current.data[0].message).toBe('line-2');
  });
});

describe('useIssueLogs', () => {
  it('caps live issue log entries locally', () => {
    const { result } = renderHook(() => useIssueLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });

    expect(streamHandles[0].url).toBe('/api/v1/issues/ENG-1/log-stream');

    act(() => {
      streamHandles[0].onOpen?.();
      for (let i = 0; i < LIVE_LOG_ENTRY_CAP + 2; i += 1) {
        streamHandles[0].onMessage({
          event: 'log',
          data: JSON.stringify({
            level: 'INFO',
            event: 'text',
            message: `issue-line-${String(i)}`,
          }),
        });
      }
    });

    expect(result.current.data).toHaveLength(LIVE_LOG_ENTRY_CAP);
    expect(result.current.data[0].message).toBe('issue-line-2');
  });
});
