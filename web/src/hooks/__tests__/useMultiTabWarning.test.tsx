// CORE-099 — a second tab gets an informational notice that states the
// measured connection limit, not an error toast.
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { renderHook } from '@testing-library/react';
import { useMultiTabWarning } from '../useMultiTabWarning';
import { useToastStore } from '../../store/toastStore';

type Listener = (e: MessageEvent) => void;
class FakeChannel {
  static peers: FakeChannel[] = [];
  onmessage: Listener | null = null;
  constructor(public name: string) {
    FakeChannel.peers.push(this);
  }
  postMessage(data: unknown) {
    for (const p of FakeChannel.peers) {
      if (p !== this && p.name === this.name) p.onmessage?.({ data } as MessageEvent);
    }
  }
  close() {
    FakeChannel.peers = FakeChannel.peers.filter((p) => p !== this);
  }
}

const original = globalThis.BroadcastChannel;
beforeEach(() => {
  FakeChannel.peers = [];
  (globalThis as { BroadcastChannel: unknown }).BroadcastChannel = FakeChannel;
  useToastStore.setState({ toasts: [], _timers: new Map() });
});
afterEach(() => {
  (globalThis as { BroadcastChannel: unknown }).BroadcastChannel = original;
});

describe('useMultiTabWarning (CORE-099)', () => {
  it('the second tab shows an info notice with the measured limit', () => {
    renderHook(() => {
      useMultiTabWarning();
    });
    expect(useToastStore.getState().toasts).toHaveLength(0);
    renderHook(() => {
      useMultiTabWarning();
    });
    const toasts = useToastStore.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].variant).toBe('info');
    expect(toasts[0].message).toMatch(/6 connections/);
    expect(toasts[0].message).toMatch(/third tab/);
  });

  it('a single tab shows nothing', () => {
    renderHook(() => {
      useMultiTabWarning();
    });
    expect(useToastStore.getState().toasts).toHaveLength(0);
  });
});
