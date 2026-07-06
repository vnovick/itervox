import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { useItervoxStore } from '../itervoxStore';

const EMPTY_SNAP = {
  running: [],
  paused: [],
  retrying: [],
  counts: { running: 0, retrying: 0, paused: 0 },
  generatedAt: '',
  maxConcurrentAgents: 3,
  // G: matches Zod default — refreshSnapshot path parses through the schema
  // and the parsed result carries this field even when the server omitted it.
  maxRetries: 5,
  // E: matches Zod defaults for switch caps.
  maxSwitchesPerIssuePerWindow: 2,
  switchWindowHours: 6,
  rateLimits: null,
};

beforeEach(() => {
  useItervoxStore.setState({
    snapshot: null,
    logs: [],
    sseConnected: false,
    selectedIdentifier: null,
    tokenSamples: [],
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('setSnapshot', () => {
  it('stores the snapshot', () => {
    useItervoxStore.getState().setSnapshot(EMPTY_SNAP as never);
    expect(useItervoxStore.getState().snapshot).toEqual(EMPTY_SNAP);
  });

  it('appends a token sample on every setSnapshot call', () => {
    const snap = { ...EMPTY_SNAP, running: [{ tokens: 500 }] };
    useItervoxStore.getState().setSnapshot(snap as never);
    expect(useItervoxStore.getState().tokenSamples).toHaveLength(1);
    expect(useItervoxStore.getState().tokenSamples[0].totalTokens).toBe(500);
  });

  it('rolls the window when MAX_TOKEN_SAMPLES (60) is reached', () => {
    // appendTokenSample dedups consecutive identical totalTokens, so each push
    // must have a unique tokens value to actually add a new sample.
    for (let i = 0; i < 61; i++) {
      const snap = { ...EMPTY_SNAP, running: [{ tokens: i + 1 }] };
      useItervoxStore.getState().setSnapshot(snap as never);
    }
    expect(useItervoxStore.getState().tokenSamples).toHaveLength(60);
  });
});

describe('appendLog', () => {
  it('appends log lines', () => {
    useItervoxStore.getState().appendLog('line 1');
    useItervoxStore.getState().appendLog('line 2');
    expect(useItervoxStore.getState().logs).toEqual(['line 1', 'line 2']);
  });

  it('does not exceed MAX_LOG_LINES (500)', () => {
    for (let i = 0; i < 505; i++) {
      useItervoxStore.getState().appendLog(`line ${String(i)}`);
    }
    expect(useItervoxStore.getState().logs).toHaveLength(500);
    expect(useItervoxStore.getState().logs.at(-1)).toBe('line 504');
  });
});

describe('clearLogs', () => {
  it('clears all logs', () => {
    useItervoxStore.getState().appendLog('a');
    useItervoxStore.getState().appendLog('b');
    useItervoxStore.getState().clearLogs();
    expect(useItervoxStore.getState().logs).toEqual([]);
  });
});

describe('setSseConnected', () => {
  it('sets sseConnected to true', () => {
    useItervoxStore.getState().setSseConnected(true);
    expect(useItervoxStore.getState().sseConnected).toBe(true);
  });

  it('sets sseConnected to false', () => {
    useItervoxStore.setState({ sseConnected: true });
    useItervoxStore.getState().setSseConnected(false);
    expect(useItervoxStore.getState().sseConnected).toBe(false);
  });
});

describe('setSelectedIdentifier', () => {
  it('stores the identifier', () => {
    useItervoxStore.getState().setSelectedIdentifier('ABC-1');
    expect(useItervoxStore.getState().selectedIdentifier).toBe('ABC-1');
  });

  it('clears when null is passed', () => {
    useItervoxStore.setState({ selectedIdentifier: 'ABC-1' });
    useItervoxStore.getState().setSelectedIdentifier(null);
    expect(useItervoxStore.getState().selectedIdentifier).toBeNull();
  });
});

describe('patchSnapshot', () => {
  it('merges partial fields into existing snapshot', () => {
    useItervoxStore.setState({ snapshot: { ...EMPTY_SNAP, agentMode: '' } as never });
    useItervoxStore.getState().patchSnapshot({ agentMode: 'teams' });
    expect(useItervoxStore.getState().snapshot?.agentMode).toBe('teams');
  });

  it('merges multiple fields at once', () => {
    useItervoxStore.setState({
      snapshot: { ...EMPTY_SNAP, activeStates: ['Todo'], terminalStates: ['Done'] } as never,
    });
    useItervoxStore.getState().patchSnapshot({ activeStates: ['Todo', 'In Progress'] });
    const snap = useItervoxStore.getState().snapshot as never as {
      activeStates: string[];
      terminalStates: string[];
    };
    expect(snap.activeStates).toEqual(['Todo', 'In Progress']);
    expect(snap.terminalStates).toEqual(['Done']); // unchanged
  });

  it('applies patch even when snapshot is null (optimistic pre-SSE update)', () => {
    useItervoxStore.setState({ snapshot: null });
    useItervoxStore.getState().patchSnapshot({ agentMode: 'teams' });
    // FE-7 fix: patch is applied to an empty base so optimistic updates are not dropped
    expect(useItervoxStore.getState().snapshot?.agentMode).toBe('teams');
  });
});

describe('refreshSnapshot', () => {
  it('fetches /api/v1/state and updates snapshot', async () => {
    const mockSnap = {
      ...EMPTY_SNAP,
      running: [
        {
          identifier: 'ENG-1',
          state: 'running',
          turnCount: 0,
          tokens: 99,
          inputTokens: 0,
          outputTokens: 0,
          lastEvent: '',
          sessionId: '',
          workerHost: '',
          backend: 'claude',
          elapsedMs: 0,
          startedAt: '2024-01-01T00:00:00Z',
        },
      ],
    };
    global.fetch = vi
      .fn()
      .mockResolvedValue({ ok: true, json: vi.fn().mockResolvedValue(mockSnap) });
    await useItervoxStore.getState().refreshSnapshot();
    expect(useItervoxStore.getState().snapshot).toEqual(mockSnap);
    expect(useItervoxStore.getState().tokenSamples).toHaveLength(1);
    expect(useItervoxStore.getState().tokenSamples[0].totalTokens).toBe(99);
  });

  it('does nothing when fetch fails', async () => {
    global.fetch = vi.fn().mockResolvedValue({ ok: false });
    await useItervoxStore.getState().refreshSnapshot();
    expect(useItervoxStore.getState().snapshot).toBeNull();
  });
});
