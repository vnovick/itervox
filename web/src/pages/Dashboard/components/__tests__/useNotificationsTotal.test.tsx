// #211 — the Notifications tab count must not re-render on frames that only
// move `generatedAt` (every SSE frame does). It used to subscribe to the whole
// snapshot, which re-rendered DashboardIssuesPanel and the board on each one.
import { beforeEach, describe, expect, it } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { useNotificationsTotal } from '../useNotificationsTotal';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { makeInputRequiredRow, makeSnapshot } from '../../../../test/fixtures/snapshots';
import { resetAllStores } from '../../../../test/resetStores';
import type { TrackerIssue } from '../../../../types/schemas';

const NO_ISSUES: TrackerIssue[] = [];

function mount() {
  let renders = 0;
  const hook = renderHook(() => {
    renders += 1;
    return useNotificationsTotal(NO_ISSUES);
  });
  return { hook, renders: () => renders };
}

beforeEach(() => {
  resetAllStores();
});

describe('useNotificationsTotal', () => {
  it('NotificationsTotal does not re-render on generatedAt-only frames', () => {
    const base = makeSnapshot({
      inputRequired: [makeInputRequiredRow({ identifier: 'DEMO-1' })],
    });
    act(() => {
      useItervoxStore.getState().setSnapshot(base);
    });
    const { hook, renders } = mount();
    expect(hook.result.current).toBe(1);
    const before = renders();

    for (let i = 1; i <= 5; i += 1) {
      act(() => {
        useItervoxStore.getState().setSnapshot({
          ...structuredClone(base),
          generatedAt: `2026-01-01T00:00:0${String(i)}Z`,
        });
      });
    }
    // Every frame was a new root object (the store did notify) …
    expect(useItervoxStore.getState().snapshot?.generatedAt).toBe('2026-01-01T00:00:05Z');
    // … but none of them re-rendered the count.
    expect(renders()).toBe(before);
    expect(hook.result.current).toBe(1);
  });

  it('re-renders when a branch it counts changes (control)', () => {
    const base = makeSnapshot({
      inputRequired: [makeInputRequiredRow({ identifier: 'DEMO-1' })],
    });
    act(() => {
      useItervoxStore.getState().setSnapshot(base);
    });
    const { hook, renders } = mount();
    const before = renders();
    act(() => {
      useItervoxStore.getState().setSnapshot({
        ...structuredClone(base),
        generatedAt: '2026-01-01T00:00:09Z',
        inputRequired: [
          makeInputRequiredRow({ identifier: 'DEMO-1' }),
          makeInputRequiredRow({ identifier: 'DEMO-2' }),
        ],
      });
    });
    expect(renders()).toBeGreaterThan(before);
    expect(hook.result.current).toBe(2);
  });

  it('is 0 before the first snapshot', () => {
    const { hook } = mount();
    expect(hook.result.current).toBe(0);
  });
});
