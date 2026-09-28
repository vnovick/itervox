import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useItervoxStore } from '../itervoxStore';
import { makeRetryRow, makeRunningRow, makeSnapshot } from '../../test/fixtures/snapshots';
import type { StateSnapshot } from '../../types/schemas';

vi.mock('../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../auth/authedFetch';

// CORE-074 — structural sharing. Every SSE push is a freshly parsed object,
// so without sharing every branch changes identity and every selector
// re-runs its consumers.

function base(overrides: Parameters<typeof makeSnapshot>[0] = {}): StateSnapshot {
  return makeSnapshot({
    running: [makeRunningRow({ identifier: 'ENG-1' }), makeRunningRow({ identifier: 'ENG-2' })],
    retrying: [makeRetryRow({ identifier: 'ENG-3' })],
    paused: ['ENG-4'],
    outboxEntries: [
      {
        id: 'o1',
        kind: 'comment',
        identifier: 'ENG-1',
        attempts: 1,
        enqueuedAt: '2026-09-01T00:00:00Z',
        nextAttemptAt: '2026-09-01T00:00:00Z',
      },
    ],
    ...overrides,
  });
}

// A deep copy, like a freshly JSON-parsed SSE frame.
const fresh = (s: StateSnapshot): StateSnapshot => structuredClone(s);

beforeEach(() => {
  useItervoxStore.setState({ snapshot: null, logs: [] });
});

afterEach(() => {
  vi.mocked(authedFetch).mockReset();
});

describe('itervoxStore', () => {
  it('setSnapshot with deep-equal payload does not notify subscribers', () => {
    const snap = base();
    useItervoxStore.getState().setSnapshot(snap);
    const first = useItervoxStore.getState().snapshot;
    const listener = vi.fn();
    const unsubscribe = useItervoxStore.subscribe(listener);
    useItervoxStore.getState().setSnapshot(fresh(snap));
    expect(listener).not.toHaveBeenCalled();
    expect(useItervoxStore.getState().snapshot).toBe(first);
    unsubscribe();
  });

  it('changed branch gets new identity while unchanged siblings keep identity', () => {
    const snap = base();
    useItervoxStore.getState().setSnapshot(snap);
    const prev = useItervoxStore.getState().snapshot as StateSnapshot;
    const next = fresh(snap);
    next.paused = ['ENG-4', 'ENG-5'];
    next.running[1] = { ...next.running[1], tokens: next.running[1].tokens + 1 };
    useItervoxStore.getState().setSnapshot(next);
    const cur = useItervoxStore.getState().snapshot as StateSnapshot;
    expect(cur).not.toBe(prev);
    expect(cur.paused).not.toBe(prev.paused);
    expect(cur.paused).toEqual(['ENG-4', 'ENG-5']);
    expect(cur.running).not.toBe(prev.running);
    expect(cur.running[0]).toBe(prev.running[0]); // unchanged row keeps identity
    expect(cur.running[1]).not.toBe(prev.running[1]);
    expect(cur.retrying).toBe(prev.retrying);
    expect(cur.outboxEntries).toBe(prev.outboxEntries);
  });

  it('timestamp-only push (generatedAt differs, all else deep-equal) keeps running/paused/outboxEntries identity while the root changes', () => {
    const snap = base();
    useItervoxStore.getState().setSnapshot(snap);
    const prev = useItervoxStore.getState().snapshot as StateSnapshot;
    const next = fresh(snap);
    next.generatedAt = '2026-09-27T00:00:05Z';
    useItervoxStore.getState().setSnapshot(next);
    const cur = useItervoxStore.getState().snapshot as StateSnapshot;
    expect(cur).not.toBe(prev);
    expect(cur.generatedAt).toBe('2026-09-27T00:00:05Z');
    expect(cur.running).toBe(prev.running);
    expect(cur.paused).toBe(prev.paused);
    expect(cur.outboxEntries).toBe(prev.outboxEntries);
  });

  it('refreshSnapshot shares structure', async () => {
    const snap = base();
    useItervoxStore.getState().setSnapshot(snap);
    const prev = useItervoxStore.getState().snapshot as StateSnapshot;
    const next = fresh(snap);
    next.generatedAt = '2026-09-27T00:00:09Z';
    vi.mocked(authedFetch).mockResolvedValue(new Response(JSON.stringify(next), { status: 200 }));
    await useItervoxStore.getState().refreshSnapshot();
    const cur = useItervoxStore.getState().snapshot as StateSnapshot;
    expect(cur).not.toBe(prev);
    expect(cur.running).toBe(prev.running);
    expect(cur.paused).toBe(prev.paused);
    expect(cur.retrying).toBe(prev.retrying);
  });
});
