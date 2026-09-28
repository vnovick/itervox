import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { useConnectionState } from '../useConnectionState';
import { useItervoxStore } from '../../store/itervoxStore';
import type { StateSnapshot } from '../../types/schemas';

// Minimal snapshot shape — useConnectionState only reads `s.snapshot !== null`.
const FAKE_SNAPSHOT = { running: [] } as unknown as StateSnapshot;

function resetStore() {
  useItervoxStore.setState({
    snapshot: null,
    sseConnected: false,
    lastMessageAt: null,
    lastSnapshotAt: null,
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  resetStore();
});

afterEach(() => {
  vi.useRealTimers();
  resetStore();
});

describe('useConnectionState', () => {
  it('reports stale after snapshot when stream stops and polling fails', () => {
    const now = Date.now();
    useItervoxStore.setState({
      snapshot: FAKE_SNAPSHOT,
      lastMessageAt: now,
      lastSnapshotAt: now,
    });
    const { result } = renderHook(() => useConnectionState());
    expect(result.current.isStale).toBe(false);

    // Advance past the stale threshold with no new message/snapshot writes —
    // both signals go quiet (stream stopped, polling failing too).
    act(() => {
      vi.setSystemTime(now + 65_000);
      vi.advanceTimersByTime(10_000);
    });

    expect(result.current.isStale).toBe(true);
  });

  it('keepalive frames keep connection live', () => {
    const now = Date.now();
    useItervoxStore.setState({
      snapshot: FAKE_SNAPSHOT,
      lastMessageAt: now,
      // No fresh real data in a while, but the channel itself is alive.
      lastSnapshotAt: now - 120_000,
    });
    const { result } = renderHook(() => useConnectionState());

    act(() => {
      vi.setSystemTime(now + 65_000);
      // A keepalive arrives and bumps lastMessageAt just before we'd cross
      // the threshold on the message side.
      useItervoxStore.getState().setLastMessageAt(Date.now());
      vi.advanceTimersByTime(10_000);
    });

    expect(result.current.isStale).toBe(false);
  });

  it('successful poll after SSE loss is not stale', () => {
    const now = Date.now();
    useItervoxStore.setState({
      snapshot: FAKE_SNAPSHOT,
      // SSE has been silent well past the threshold...
      lastMessageAt: now - 120_000,
      lastSnapshotAt: now - 120_000,
    });
    const { result } = renderHook(() => useConnectionState());

    act(() => {
      vi.setSystemTime(now + 5_000);
      // ...but the polling fallback just succeeded.
      useItervoxStore.getState().setLastSnapshotAt(Date.now());
      vi.advanceTimersByTime(10_000);
    });

    expect(result.current.isStale).toBe(false);
  });

  it('recovers to live when a snapshot arrives', () => {
    const now = Date.now();
    useItervoxStore.setState({
      snapshot: FAKE_SNAPSHOT,
      lastMessageAt: now - 120_000,
      lastSnapshotAt: now - 120_000,
    });
    const { result } = renderHook(() => useConnectionState());

    act(() => {
      vi.setSystemTime(now + 65_000);
      vi.advanceTimersByTime(10_000);
    });
    expect(result.current.isStale).toBe(true);

    act(() => {
      const recoveredAt = Date.now();
      useItervoxStore.getState().setLastMessageAt(recoveredAt);
      useItervoxStore.getState().setLastSnapshotAt(recoveredAt);
      vi.advanceTimersByTime(10_000);
    });

    expect(result.current.isStale).toBe(false);
  });

  // CORE-023 / M0-close G5 — SSE never opened (lastMessageAt stays null for
  // the life of the page) and the polling fallback is the only signal. A
  // null SSE timestamp means "the channel never spoke", so it must count as
  // silent rather than vetoing the stale verdict forever.
  it('reports stale when SSE never opened and polling has gone silent', () => {
    const now = Date.now();
    useItervoxStore.setState({
      snapshot: FAKE_SNAPSHOT,
      sseConnected: false,
      lastMessageAt: null,
      lastSnapshotAt: now,
    });
    const { result } = renderHook(() => useConnectionState());
    expect(result.current.isStale).toBe(false);

    act(() => {
      vi.setSystemTime(now + 65_000);
      vi.advanceTimersByTime(10_000);
    });

    expect(result.current.isStale).toBe(true);
  });

  it('is not stale when SSE never opened but polling keeps delivering fresh snapshots', () => {
    const start = Date.now();
    useItervoxStore.setState({
      snapshot: FAKE_SNAPSHOT,
      sseConnected: false,
      lastMessageAt: null,
      lastSnapshotAt: start,
    });
    const { result } = renderHook(() => useConnectionState());

    // Poll every 3s for well past the stale threshold; SSE never speaks.
    for (let elapsed = 3_000; elapsed <= 120_000; elapsed += 3_000) {
      act(() => {
        vi.setSystemTime(start + elapsed);
        useItervoxStore.getState().setLastSnapshotAt(Date.now());
        vi.advanceTimersByTime(3_000);
      });
      expect(result.current.isStale).toBe(false);
    }
    expect(useItervoxStore.getState().lastMessageAt).toBeNull();
  });

  it('is not stale before any snapshot has landed (isOffline covers that case)', () => {
    const { result } = renderHook(() => useConnectionState());
    expect(result.current.hasSnapshot).toBe(false);
    expect(result.current.isStale).toBe(false);
  });

  it('exposes lastSnapshotAt for the caller to render a data age', () => {
    const now = Date.now();
    useItervoxStore.setState({ snapshot: FAKE_SNAPSHOT, lastSnapshotAt: now });
    const { result } = renderHook(() => useConnectionState());
    expect(result.current.lastSnapshotAt).toBe(now);
  });
});
