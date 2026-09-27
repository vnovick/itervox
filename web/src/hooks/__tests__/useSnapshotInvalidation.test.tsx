import { act, renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useSnapshotInvalidation } from '../useSnapshotInvalidation';
import { useItervoxStore } from '../../store/itervoxStore';
import { makeSnapshot } from '../../test/fixtures/snapshots';
import type { StateSnapshot } from '../../types/schemas';

function wrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  };
}

// Counts how often the fingerprint reads the snapshot: every build maps
// `running`, so reads of that property == fingerprint computations.
function countingSnapshot(base: StateSnapshot): { snapshot: StateSnapshot; reads: () => number } {
  let reads = 0;
  const snapshot = { ...base };
  const running = base.running;
  Object.defineProperty(snapshot, 'running', {
    enumerable: true,
    get: () => {
      reads += 1;
      return running;
    },
  });
  return { snapshot, reads: () => reads };
}

beforeEach(() => {
  useItervoxStore.setState({ snapshot: null, logs: [] });
});

describe('useSnapshotInvalidation', () => {
  it('fingerprint is not recomputed when snapshot identity is unchanged', () => {
    const client = new QueryClient();
    const { snapshot, reads } = countingSnapshot(makeSnapshot());
    useItervoxStore.setState({ snapshot });
    renderHook(
      () => {
        useSnapshotInvalidation();
      },
      { wrapper: wrapper(client) },
    );
    const afterMount = reads();
    expect(afterMount).toBeGreaterThan(0);

    // Unrelated store traffic (log lines, keepalives) must not rebuild it.
    act(() => {
      for (let i = 0; i < 25; i++) {
        useItervoxStore.getState().appendLog(`line ${String(i)}`);
        useItervoxStore.getState().setLastMessageAt(i);
      }
    });
    expect(reads()).toBe(afterMount);
  });

  it('invalidates the issue queries when the fingerprint changes', () => {
    const client = new QueryClient();
    const spy = vi.spyOn(client, 'invalidateQueries');
    useItervoxStore.setState({ snapshot: makeSnapshot({ running: [] }) });
    renderHook(
      () => {
        useSnapshotInvalidation();
      },
      { wrapper: wrapper(client) },
    );
    expect(spy).not.toHaveBeenCalled();
    act(() => {
      useItervoxStore.setState({ snapshot: makeSnapshot({ paused: ['ENG-9'] }) });
    });
    expect(spy).toHaveBeenCalledWith({ queryKey: ['issues'] });
  });
});
