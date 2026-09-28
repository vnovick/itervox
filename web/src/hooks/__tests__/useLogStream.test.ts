import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useLogStream } from '../useLogStream';
import { useItervoxStore } from '../../store/itervoxStore';

interface Handle {
  url: string;
  onOpen?: () => void;
  onMessage: (msg: { event?: string; data: string; id?: string }) => void;
  close: ReturnType<typeof vi.fn>;
}

const handles: Handle[] = [];

vi.mock('../../auth/authedEventStream', () => ({
  openAuthedEventStream: (url: string, opts: Omit<Handle, 'url' | 'close'>) => {
    const h: Handle = { url, onOpen: opts.onOpen, onMessage: opts.onMessage, close: vi.fn() };
    handles.push(h);
    return () => {
      h.close();
    };
  },
}));

beforeEach(() => {
  // CORE-075: lines are applied once per animation frame.
  vi.useFakeTimers();
  handles.length = 0;
  useItervoxStore.getState().clearLogs();
});

afterEach(() => {
  vi.useRealTimers();
});

// M1-B1 fix round 1 (I2): /api/v1/logs replays the last 16 KiB of the daemon
// log on every connect, with no ids and no Last-Event-ID resume. Since
// CORE-025 closes SSE streams on every config reload, appending blindly made
// every reload re-append the tail already on screen.
describe('useLogStream', () => {
  const tail = ['line-1', 'line-2', 'line-3'];
  const sendTail = (h: Handle) => {
    for (const l of tail) h.onMessage({ event: 'log', data: l });
  };

  it('does not duplicate the replayed tail on reconnect', () => {
    renderHook(() => {
      useLogStream();
    });
    const h = handles[0];
    act(() => {
      h.onOpen?.();
      sendTail(h);
      vi.advanceTimersToNextFrame();
    });
    expect(useItervoxStore.getState().logs).toEqual(tail);

    // Reload: the server closes the stream; the client reconnects and the
    // new generation replays the same tail, plus a new line.
    act(() => {
      h.onOpen?.();
      sendTail(h);
      h.onMessage({ event: 'log', data: 'line-4' });
      vi.advanceTimersToNextFrame();
    });
    expect(useItervoxStore.getState().logs).toEqual([...tail, 'line-4']);
  });

  it('ignores non-log events and uses the identifier filter URL', () => {
    renderHook(() => {
      useLogStream('ENG 1');
    });
    expect(handles[0].url).toBe('/api/v1/logs?identifier=ENG%201');
    act(() => {
      handles[0].onOpen?.();
      handles[0].onMessage({ event: 'keepalive', data: '{}' });
      handles[0].onMessage({ event: 'log', data: 'kept' });
      vi.advanceTimersToNextFrame();
    });
    expect(useItervoxStore.getState().logs).toEqual(['kept']);
  });

  // CORE-075 — lines are buffered and appended once per animation frame, so
  // a burst of N lines costs one store notification instead of N.
  it('batched lines are appended in arrival order and flushed on unmount', () => {
    vi.useFakeTimers();
    try {
      const notify = vi.fn();
      const unsubscribe = useItervoxStore.subscribe(notify);
      const { unmount } = renderHook(() => {
        useLogStream();
      });
      const h = handles[0];
      act(() => {
        h.onOpen?.();
      });
      notify.mockClear();
      act(() => {
        for (const l of ['a', 'b', 'c']) h.onMessage({ event: 'log', data: l });
      });
      // Nothing is applied before the frame.
      expect(useItervoxStore.getState().logs).toEqual([]);
      act(() => {
        vi.advanceTimersToNextFrame();
      });
      expect(useItervoxStore.getState().logs).toEqual(['a', 'b', 'c']);
      expect(notify).toHaveBeenCalledTimes(1);

      // Lines still waiting for a frame are flushed, in order, on unmount.
      act(() => {
        h.onMessage({ event: 'log', data: 'd' });
        h.onMessage({ event: 'log', data: 'e' });
      });
      unmount();
      expect(useItervoxStore.getState().logs).toEqual(['a', 'b', 'c', 'd', 'e']);
      expect(h.close).toHaveBeenCalledTimes(1);
      unsubscribe();
    } finally {
      vi.useRealTimers();
    }
  });
});
