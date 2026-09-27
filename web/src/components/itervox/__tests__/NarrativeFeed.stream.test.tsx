import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { NarrativeFeed } from '../NarrativeFeed';
import { useItervoxStore } from '../../../store/itervoxStore';

// CORE-075 — the global /api/v1/logs stream belongs to NarrativeFeed (its
// only reader), not to the app root. CORE-069 — its lines carry a monotonic
// client id so the Terminal keys stay stable when the 20-line window moves.

interface Handle {
  url: string;
  onOpen?: () => void;
  onMessage: (msg: { event?: string; data: string; id?: string }) => void;
  close: ReturnType<typeof vi.fn>;
}
const handles: Handle[] = [];

vi.mock('../../../auth/authedEventStream', () => ({
  openAuthedEventStream: (url: string, opts: Omit<Handle, 'url' | 'close'>) => {
    const h: Handle = { url, onOpen: opts.onOpen, onMessage: opts.onMessage, close: vi.fn() };
    handles.push(h);
    return () => {
      h.close();
    };
  },
}));

beforeEach(() => {
  vi.useFakeTimers();
  handles.length = 0;
  useItervoxStore.getState().clearLogs();
});

afterEach(() => {
  vi.useRealTimers();
});

function emit(h: Handle, lines: string[]) {
  act(() => {
    for (const data of lines) h.onMessage({ event: 'log', data });
    vi.advanceTimersToNextFrame();
  });
}

describe('NarrativeFeed', () => {
  it('opens the global log stream on mount and closes it on unmount', () => {
    const { unmount } = render(<NarrativeFeed />);
    expect(handles).toHaveLength(1);
    expect(handles[0].url).toBe('/api/v1/logs');
    expect(handles[0].close).not.toHaveBeenCalled();
    unmount();
    expect(handles[0].close).toHaveBeenCalledTimes(1);
  });

  it('remount clears the buffer before reopening so replayed tail lines are not duplicated', () => {
    const tail = ['line-1', 'line-2', 'line-3'];
    const first = render(<NarrativeFeed />);
    act(() => {
      handles[0].onOpen?.();
    });
    emit(handles[0], tail);
    expect(useItervoxStore.getState().logs).toEqual(tail);
    first.unmount();

    // The server replays the same 16 KiB tail on every connect.
    render(<NarrativeFeed />);
    expect(handles).toHaveLength(2);
    act(() => {
      handles[1].onOpen?.();
    });
    emit(handles[1], tail);
    expect(useItervoxStore.getState().logs).toEqual(tail);
    expect(screen.getAllByText(/^line-\d$/)).toHaveLength(3);
  });

  it('entries carry stable client ids across a capped-window advance', () => {
    render(<NarrativeFeed />);
    emit(
      handles[0],
      Array.from({ length: 20 }, (_, i) => `event ${String(i + 1)}`),
    );
    const before = screen.getByText('event 10').closest('[data-seq]');
    expect(before).not.toBeNull();
    const seq = before?.getAttribute('data-seq');

    // The 20-line window advances by one at equal length.
    emit(handles[0], ['event 21']);
    expect(screen.queryByText('event 1')).not.toBeInTheDocument();
    const after = screen.getByText('event 10').closest('[data-seq]');
    expect(after).toBe(before); // same keyed row, not a re-used neighbour
    expect(after?.getAttribute('data-seq')).toBe(seq);
    const seqs = Array.from(document.querySelectorAll('[data-seq]')).map((el) =>
      Number(el.getAttribute('data-seq')),
    );
    expect(seqs).toHaveLength(20);
    expect(seqs.every((v, i) => i === 0 || v === seqs[i - 1] + 1)).toBe(true);
  });
});
