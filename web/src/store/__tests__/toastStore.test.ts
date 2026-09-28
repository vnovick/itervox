import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useToastStore } from '../toastStore';
import { TOAST_DISMISS_MS } from '../../utils/timings';

function reset() {
  for (const t of useToastStore.getState()._timers.values()) clearTimeout(t);
  useToastStore.setState({ toasts: [], _timers: new Map() });
}

function toasts() {
  return useToastStore.getState().toasts;
}

describe('toastStore', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    reset();
  });
  afterEach(() => {
    reset();
    vi.useRealTimers();
  });

  it('keeps the addToast(message, variant) signature and defaults to error', () => {
    useToastStore.getState().addToast('boom');
    expect(toasts()).toHaveLength(1);
    expect(toasts()[0]).toMatchObject({ message: 'boom', variant: 'error', count: 1 });
  });

  it('dedupes identical messages', () => {
    const { addToast } = useToastStore.getState();
    // Same message + variant → one entry with a counter.
    addToast('Saved', 'success');
    addToast('Saved', 'success');
    expect(toasts()).toHaveLength(1);
    expect(toasts()[0].count).toBe(2);

    // Same message, different variant → a separate entry.
    addToast('Saved', 'info');
    expect(toasts()).toHaveLength(2);
    expect(toasts().map((t) => t.variant)).toEqual(['success', 'info']);

    // A repeated success restarts its dismiss timer.
    vi.advanceTimersByTime(TOAST_DISMISS_MS - 100);
    addToast('Saved', 'success');
    expect(toasts().find((t) => t.variant === 'success')?.count).toBe(3);
    vi.advanceTimersByTime(200);
    // The info toast (never repeated) has expired; the success is still up.
    expect(toasts().map((t) => t.variant)).toEqual(['success']);
    vi.advanceTimersByTime(TOAST_DISMISS_MS);
    expect(toasts()).toHaveLength(0);
  });

  it('error toasts do not auto-dismiss', () => {
    const { addToast } = useToastStore.getState();
    addToast('failure', 'error');
    addToast('failure', 'error');
    vi.advanceTimersByTime(TOAST_DISMISS_MS * 10);
    expect(toasts()).toHaveLength(1);
    expect(toasts()[0]).toMatchObject({ variant: 'error', count: 2 });
    expect(useToastStore.getState()._timers.size).toBe(0);

    useToastStore.getState().removeToast(toasts()[0].id);
    expect(toasts()).toHaveLength(0);
  });

  it('success and info toasts still auto-dismiss', () => {
    const { addToast } = useToastStore.getState();
    addToast('ok', 'success');
    addToast('fyi', 'info');
    vi.advanceTimersByTime(TOAST_DISMISS_MS + 1);
    expect(toasts()).toHaveLength(0);
  });

  it('cap of 3 is per region: fourth error replaces oldest error, fourth success evicts oldest success, three sticky errors do not block a success', () => {
    const { addToast } = useToastStore.getState();
    addToast('e1', 'error');
    addToast('e2', 'error');
    addToast('e3', 'error');
    addToast('e4', 'error');
    expect(toasts().map((t) => t.message)).toEqual(['e2', 'e3', 'e4']);

    // Three sticky errors do not block a success.
    addToast('s1', 'success');
    expect(toasts().map((t) => t.message)).toEqual(['e2', 'e3', 'e4', 's1']);

    addToast('s2', 'success');
    addToast('i3', 'info');
    addToast('s4', 'success');
    // The status region (success + info) holds three; its oldest went.
    expect(toasts().map((t) => t.message)).toEqual(['e2', 'e3', 'e4', 's2', 'i3', 's4']);
    // The evicted toast's timer was cleared, not left to fire later.
    expect(useToastStore.getState()._timers.size).toBe(3);
  });

  it('removeToast clears the pending timer', () => {
    useToastStore.getState().addToast('ok', 'success');
    const id = toasts()[0].id;
    useToastStore.getState().removeToast(id);
    expect(useToastStore.getState()._timers.size).toBe(0);
    vi.advanceTimersByTime(TOAST_DISMISS_MS * 2);
    expect(toasts()).toHaveLength(0);
  });
});
