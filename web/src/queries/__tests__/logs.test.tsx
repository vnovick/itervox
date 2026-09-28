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

// CORE-027 — live log panels keep already-shown history across an SSE
// reconnect. The server resumes after the Last-Event-ID the library resends,
// so clearing on every onOpen dropped lines for good; replayed ids must be
// de-duplicated, a server `gap` frame must be shown, and a stream generation
// change (daemon restart / reload: a new logbuffer epoch) must replace the
// list instead of de-duplicating against numbering that no longer applies.

function logFrame(event: 'log' | 'sublog', id: string, message: string) {
  return {
    event,
    id,
    data: JSON.stringify({ level: 'INFO', event: 'text', message }),
  };
}

function messages(data: { message: string }[] | undefined): string[] {
  return (data ?? []).map((e) => e.message);
}

describe('useIssueLogs reconnect (CORE-027)', () => {
  it('keeps existing lines across reconnect', () => {
    const { result } = renderHook(() => useIssueLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });
    const h = streamHandles[0];
    act(() => {
      h.onOpen?.();
      h.onMessage(logFrame('log', '7-1', 'one'));
      h.onMessage(logFrame('log', '7-2', 'two'));
      h.onMessage(logFrame('log', '7-3', 'three'));
    });
    // Transport drop, library retry with Last-Event-ID: 7-3.
    act(() => {
      h.onDisconnect?.();
      h.onOpen?.();
      h.onMessage(logFrame('log', '7-3', 'three')); // replayed id
      h.onMessage(logFrame('log', '7-4', 'four'));
    });
    expect(messages(result.current.data)).toEqual(['one', 'two', 'three', 'four']);
    expect(result.current.isError).toBe(false);
  });

  it('clears lines when identifier changes', () => {
    const { result, rerender } = renderHook(({ id }) => useIssueLogs(id, true), {
      wrapper: createWrapper(),
      initialProps: { id: 'ENG-1' },
    });
    act(() => {
      streamHandles[0].onOpen?.();
      streamHandles[0].onMessage(logFrame('log', '7-1', 'eng-1 line'));
    });
    expect(messages(result.current.data)).toEqual(['eng-1 line']);

    rerender({ id: 'ENG-2' });
    expect(result.current.data).toEqual([]);
    expect(streamHandles[0].close).toHaveBeenCalled();
    expect(streamHandles[1].url).toBe('/api/v1/issues/ENG-2/log-stream');
    act(() => {
      streamHandles[1].onOpen?.();
      streamHandles[1].onMessage(logFrame('log', '7-1', 'eng-2 line'));
    });
    expect(messages(result.current.data)).toEqual(['eng-2 line']);
  });

  it('resets on generation change', () => {
    const { result } = renderHook(() => useIssueLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });
    const h = streamHandles[0];
    act(() => {
      h.onOpen?.();
      h.onMessage(logFrame('log', '7-1', 'old-1'));
      h.onMessage(logFrame('log', '7-2', 'old-2'));
    });
    // Daemon restarted: the retry's Last-Event-ID carries a foreign epoch, so
    // the server sends a gap and replays its window under epoch 9. The gap's
    // seq (5) is ABOVE the last one shown (2), so only the generation check —
    // not the overlap rule — can tell that the old lines must go.
    act(() => {
      h.onOpen?.();
      h.onMessage({ event: 'gap', id: '9-5', data: '{}' });
      h.onMessage(logFrame('log', '9-6', 'new-1'));
      h.onMessage(logFrame('log', '9-7', 'new-2'));
    });
    const got = messages(result.current.data);
    expect(got).not.toContain('old-1');
    expect(got.slice(1)).toEqual(['new-1', 'new-2']);
    expect(result.current.data[0].event).toBe('warn');
    expect(got[0]).toMatch(/reset/i);
  });

  it('shows a marker where the server reports skipped lines', () => {
    const { result } = renderHook(() => useIssueLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });
    const h = streamHandles[0];
    act(() => {
      h.onOpen?.();
      h.onMessage(logFrame('log', '7-1', 'one'));
      h.onMessage(logFrame('log', '7-3', 'three'));
    });
    // Reconnect after the cursor fell out of the retained window: gap stamped
    // with the seq just before the replay, then the replay.
    act(() => {
      h.onOpen?.();
      h.onMessage({ event: 'gap', id: '7-10', data: '{}' });
      h.onMessage(logFrame('log', '7-11', 'eleven'));
    });
    const got = messages(result.current.data);
    expect(got[0]).toBe('one');
    expect(got[1]).toBe('three');
    expect(got[2]).toMatch(/7 log lines were skipped/);
    expect(result.current.data[2].event).toBe('warn');
    expect(got[3]).toBe('eleven');
  });

  it('replaces the list when a same-generation gap replays lines it already shows', () => {
    const { result } = renderHook(() => useIssueLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });
    const h = streamHandles[0];
    act(() => {
      h.onOpen?.();
      h.onMessage(logFrame('log', '7-4', 'four'));
      h.onMessage(logFrame('log', '7-5', 'five'));
      h.onOpen?.();
      h.onMessage({ event: 'gap', id: '7-3', data: '{}' });
      h.onMessage(logFrame('log', '7-4', 'four'));
      h.onMessage(logFrame('log', '7-5', 'five'));
    });
    const got = messages(result.current.data);
    expect(got.slice(1)).toEqual(['four', 'five']);
    expect(got[0]).toMatch(/reset/i);
  });

  it('a new stream for the same issue replaces the list with its replay', () => {
    const { result, rerender } = renderHook(({ live }) => useIssueLogs('ENG-1', live), {
      wrapper: createWrapper(),
      initialProps: { live: true },
    });
    act(() => {
      streamHandles[0].onOpen?.();
      streamHandles[0].onMessage(logFrame('log', '7-1', 'one'));
    });
    rerender({ live: false });
    rerender({ live: true });
    act(() => {
      streamHandles[1].onOpen?.();
      streamHandles[1].onMessage(logFrame('log', '7-5', 'five'));
    });
    expect(messages(result.current.data)).toEqual(['five']);
  });

  it('appends frames whose id cannot be parsed', () => {
    const { result } = renderHook(() => useIssueLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });
    act(() => {
      streamHandles[0].onOpen?.();
      streamHandles[0].onMessage({
        event: 'log',
        data: JSON.stringify({ level: 'INFO', event: 'text', message: 'no id' }),
      });
      streamHandles[0].onMessage({ event: 'gap', data: '{}' });
      streamHandles[0].onMessage({ event: 'log', data: 'not json' });
    });
    const got = messages(result.current.data);
    expect(got[0]).toBe('no id');
    expect(got).toHaveLength(2);
    expect(got[1]).toMatch(/skipped/);
  });
});

describe('useSubagentLogs reconnect (CORE-027)', () => {
  it('keeps existing lines across reconnect', () => {
    const { result } = renderHook(() => useSubagentLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });
    const h = streamHandles[0];
    act(() => {
      h.onOpen?.();
      h.onMessage(logFrame('sublog', '4101-1', 'one'));
      h.onMessage(logFrame('sublog', '4101-2', 'two'));
    });
    act(() => {
      h.onDisconnect?.();
      h.onOpen?.();
      h.onMessage(logFrame('sublog', '4101-2', 'two')); // replayed id
      h.onMessage(logFrame('sublog', '4101-3', 'three'));
    });
    expect(messages(result.current.data)).toEqual(['one', 'two', 'three']);
  });

  it('clears lines when identifier changes', () => {
    const { result, rerender } = renderHook(({ id }) => useSubagentLogs(id, true), {
      wrapper: createWrapper(),
      initialProps: { id: 'ENG-1' },
    });
    act(() => {
      streamHandles[0].onOpen?.();
      streamHandles[0].onMessage(logFrame('sublog', '4101-1', 'eng-1'));
    });
    rerender({ id: 'ENG-2' });
    expect(result.current.data).toEqual([]);
  });

  // CORE-153 — sublog ids are `<epoch>-<seq>`; a new session-file epoch is
  // announced by a gap frame `<epoch>-0` and a full replay.
  it('replaces the list when the server restarts the session numbering', () => {
    const { result } = renderHook(() => useSubagentLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });
    const h = streamHandles[0];
    act(() => {
      h.onOpen?.();
      h.onMessage(logFrame('sublog', '4101-1', 'one'));
      h.onMessage(logFrame('sublog', '4101-2', 'two'));
      h.onOpen?.();
      h.onMessage({ event: 'gap', id: '4102-0', data: '{}' });
      h.onMessage(logFrame('sublog', '4102-1', 'fresh'));
    });
    const got = messages(result.current.data);
    expect(got[0]).toMatch(/reset/i);
    expect(got.slice(1)).toEqual(['fresh']);
  });

  it('handles a same-epoch sublog gap signal without duplicating the replay', () => {
    const { result } = renderHook(() => useSubagentLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });
    const h = streamHandles[0];
    act(() => {
      h.onOpen?.();
      h.onMessage(logFrame('sublog', '4101-1', 'one'));
      h.onMessage(logFrame('sublog', '4101-2', 'two'));
    });
    // Reconnect with a cursor the server cannot resume from: gap `<epoch>-0`
    // and a full replay of the same epoch.
    act(() => {
      h.onDisconnect?.();
      h.onOpen?.();
      h.onMessage({ event: 'gap', id: '4101-0', data: '{}' });
      h.onMessage(logFrame('sublog', '4101-1', 'one'));
      h.onMessage(logFrame('sublog', '4101-2', 'two'));
      h.onMessage(logFrame('sublog', '4101-3', 'three'));
    });
    const got = messages(result.current.data);
    expect(got[0]).toMatch(/reset/i);
    expect(got.slice(1)).toEqual(['one', 'two', 'three']);
  });

  it('a sublog frame from a new epoch without a gap still replaces the list', () => {
    const { result } = renderHook(() => useSubagentLogs('ENG-1', true), {
      wrapper: createWrapper(),
    });
    const h = streamHandles[0];
    act(() => {
      h.onOpen?.();
      h.onMessage(logFrame('sublog', '4101-1', 'one'));
      h.onMessage(logFrame('sublog', '4102-1', 'other set'));
    });
    const got = messages(result.current.data);
    expect(got[0]).toMatch(/reset/i);
    expect(got.slice(1)).toEqual(['other set']);
  });
});
