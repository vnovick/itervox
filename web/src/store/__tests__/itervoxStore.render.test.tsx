import { act, render } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../itervoxStore';
import { makeRunningRow, makeSnapshot } from '../../test/fixtures/snapshots';
import { EMPTY_RUNNING } from '../../utils/constants';

// CORE-074 — render-count proof at the component level: a subscriber whose
// selected slice did not change is not re-rendered by an SSE push, both for
// a single-branch selector and for a useShallow multi-branch selector (the
// Dashboard pattern).

beforeEach(() => {
  useItervoxStore.setState({ snapshot: null });
});

describe('itervoxStore', () => {
  it('components selecting unchanged slices do not re-render across SSE updates', () => {
    const snap = makeSnapshot({
      running: [makeRunningRow({ identifier: 'ENG-1' })],
      paused: ['ENG-2'],
    });
    useItervoxStore.getState().setSnapshot(snap);
    const runningRender = vi.fn();
    const multiRender = vi.fn();
    const renders = () => ({
      running: runningRender.mock.calls.length,
      multi: multiRender.mock.calls.length,
    });

    function RunningOnly() {
      const running = useItervoxStore((s) => s.snapshot?.running ?? EMPTY_RUNNING);
      runningRender();
      return <span>{running.length}</span>;
    }
    function Multi() {
      const { running, retrying } = useItervoxStore(
        useShallow((s) => ({
          running: s.snapshot?.running ?? EMPTY_RUNNING,
          retrying: s.snapshot?.retrying,
        })),
      );
      multiRender();
      return <span>{running.length + (retrying?.length ?? 0)}</span>;
    }
    render(
      <>
        <RunningOnly />
        <Multi />
      </>,
    );
    expect(renders()).toEqual({ running: 1, multi: 1 });

    // Three pushes: timestamp only, then a change in a branch neither reads.
    act(() => {
      const a = structuredClone(snap);
      a.generatedAt = '2026-09-27T00:00:01Z';
      useItervoxStore.getState().setSnapshot(a);
      const b = structuredClone(a);
      b.generatedAt = '2026-09-27T00:00:02Z';
      b.paused = ['ENG-2', 'ENG-3'];
      useItervoxStore.getState().setSnapshot(b);
    });
    expect(renders()).toEqual({ running: 1, multi: 1 });

    // A change in the selected branch does re-render.
    act(() => {
      const current = useItervoxStore.getState().snapshot;
      if (!current) throw new Error('snapshot missing');
      const c = structuredClone(current);
      c.running = [...c.running, makeRunningRow({ identifier: 'ENG-4' })];
      useItervoxStore.getState().setSnapshot(c);
    });
    expect(renders()).toEqual({ running: 2, multi: 2 });
  });
});
