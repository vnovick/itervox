import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, act } from '@testing-library/react';

// CORE-047 — with the real store (not the mocked one in useItervoxSSE.test.ts):
// malformed snapshots on the SSE stream or the poll fallback increment the
// shared schemaDriftCount, and the SchemaDriftBanner appears at 3 — in a
// production build (import.meta.env.DEV false), where the old code only had
// a DEV console.warn.

interface Handle {
  onOpen?: () => void;
  onMessage: (msg: { event?: string; data: string }) => void;
  onDisconnect?: () => void;
}
const handles: Handle[] = [];
vi.mock('../../auth/authedEventStream', () => ({
  openAuthedEventStream: (_url: string, opts: Handle) => {
    handles.push(opts);
    return () => undefined;
  },
}));

const authedFetch = vi.fn();
vi.mock('../../auth/authedFetch', () => ({
  authedFetch: (...args: unknown[]) => authedFetch(...args) as Promise<Response>,
}));

const reportClientError = vi.fn();
vi.mock('../../lib/clientErrorReporter', () => ({
  reportClientError: (...args: unknown[]) => {
    reportClientError(...args);
  },
}));

import { useItervoxSSE } from '../useItervoxSSE';
import { useItervoxStore } from '../../store/itervoxStore';
import { SchemaDriftBanner } from '../../components/itervox/SchemaDriftBanner';

const VALID = {
  generatedAt: 'x',
  counts: { running: 0, retrying: 0, paused: 0 },
  running: [],
  retrying: [],
  paused: [],
  maxConcurrentAgents: 3,
  maxRetries: 5,
  maxSwitchesPerIssuePerWindow: 2,
  switchWindowHours: 6,
  rateLimits: null,
};
const DRIFTED = JSON.stringify({ ...VALID, counts: 'nope' });

function Harness() {
  useItervoxSSE();
  return <SchemaDriftBanner />;
}

beforeEach(() => {
  vi.stubEnv('DEV', false);
  vi.useFakeTimers();
  handles.length = 0;
  authedFetch.mockReset();
  reportClientError.mockReset();
  // Poll fallback: a transport failure (never counted as drift).
  authedFetch.mockResolvedValue(new Response('', { status: 502 }));
  useItervoxStore.setState({ snapshot: null, schemaDriftCount: 0, lastSchemaDrift: null });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllEnvs();
});

describe('useItervoxSSE', () => {
  it('shows schema drift banner after 3 parse failures', async () => {
    render(<Harness />);
    await act(async () => {
      await Promise.resolve();
    });
    const h = handles[0];
    act(() => {
      h.onOpen?.();
      h.onMessage({ data: DRIFTED });
      h.onMessage({ data: '{broken json' });
    });
    expect(screen.queryByTestId('schema-drift-banner')).toBeNull();
    // A keepalive does not reset the streak.
    act(() => {
      h.onMessage({ event: 'keepalive', data: 'ping' });
      h.onMessage({ data: DRIFTED });
    });
    const banner = screen.getByTestId('schema-drift-banner');
    expect(banner.getAttribute('role')).toBe('alert');
    expect(useItervoxStore.getState().schemaDriftCount).toBe(3);
    expect(reportClientError).toHaveBeenCalledTimes(1);

    // A good snapshot clears it.
    act(() => {
      h.onMessage({ data: JSON.stringify(VALID) });
    });
    expect(screen.queryByTestId('schema-drift-banner')).toBeNull();
    expect(useItervoxStore.getState().schemaDriftCount).toBe(0);
  });

  it('counts poll-path parse failures into the same counter', async () => {
    authedFetch.mockImplementation(() => Promise.resolve(new Response(DRIFTED, { status: 200 })));
    render(<Harness />);
    // startPoll fires once immediately, then every 3s.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6_100);
    });
    expect(useItervoxStore.getState().schemaDriftCount).toBeGreaterThanOrEqual(3);
    expect(useItervoxStore.getState().lastSchemaDrift?.reason).toBe('schema');
    expect(screen.getByTestId('schema-drift-banner')).toBeTruthy();
  });
});
