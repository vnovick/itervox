import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// CORE-047 — refreshSnapshot used to swallow a Zod failure behind a DEV-only
// console.warn, so a drifted snapshot left production silently stale. It now
// counts consecutive parse failures in the shared schemaDriftCount (the same
// counter the SSE and poll paths use) and reports the drift once per streak.

const reportClientError = vi.fn();
vi.mock('../../lib/clientErrorReporter', () => ({
  reportClientError: (...args: unknown[]) => {
    reportClientError(...args);
  },
}));

const authedFetch = vi.fn();
vi.mock('../../auth/authedFetch', () => ({
  authedFetch: (...args: unknown[]) => authedFetch(...args) as Promise<Response>,
}));

import { useItervoxStore, SCHEMA_DRIFT_BANNER_THRESHOLD } from '../itervoxStore';

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

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

beforeEach(() => {
  vi.stubEnv('DEV', false);
  reportClientError.mockReset();
  authedFetch.mockReset();
  useItervoxStore.setState({ snapshot: null, schemaDriftCount: 0, lastSchemaDrift: null });
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe('itervoxStore', () => {
  it('refreshSnapshot increments schemaDriftCount', async () => {
    authedFetch.mockImplementation(() => Promise.resolve(json({ generatedAt: 42 })));
    await useItervoxStore.getState().refreshSnapshot();
    expect(useItervoxStore.getState().schemaDriftCount).toBe(1);
    expect(useItervoxStore.getState().lastSchemaDrift?.reason).toBe('schema');
    await useItervoxStore.getState().refreshSnapshot();
    expect(useItervoxStore.getState().schemaDriftCount).toBe(2);
  });

  it('records a JSON-syntax failure with reason json', async () => {
    authedFetch.mockResolvedValue(new Response('{not json', { status: 200 }));
    await useItervoxStore.getState().refreshSnapshot();
    expect(useItervoxStore.getState().schemaDriftCount).toBe(1);
    expect(useItervoxStore.getState().lastSchemaDrift?.reason).toBe('json');
  });

  it('does not count a transport failure as drift', async () => {
    authedFetch.mockResolvedValue(json({ error: { code: 'x', message: 'x' } }, 500));
    await useItervoxStore.getState().refreshSnapshot();
    expect(useItervoxStore.getState().schemaDriftCount).toBe(0);
  });

  it('resets only on a successfully parsed snapshot', async () => {
    authedFetch.mockImplementation(() => Promise.resolve(json({ generatedAt: 42 })));
    await useItervoxStore.getState().refreshSnapshot();
    authedFetch.mockResolvedValue(json(VALID));
    await useItervoxStore.getState().refreshSnapshot();
    expect(useItervoxStore.getState().schemaDriftCount).toBe(0);
    expect(useItervoxStore.getState().snapshot?.maxRetries).toBe(5);
  });

  it('reports the drift once when the streak reaches the banner threshold', async () => {
    authedFetch.mockImplementation(() => Promise.resolve(json({ generatedAt: 42 })));
    for (let i = 0; i < SCHEMA_DRIFT_BANNER_THRESHOLD + 2; i++) {
      await useItervoxStore.getState().refreshSnapshot();
    }
    expect(reportClientError).toHaveBeenCalledTimes(1);
    const report = reportClientError.mock.calls[0][0] as { kind: string; message: string };
    expect(report.kind).toBe('schema');
    // Paths and issue codes only — never snapshot values.
    expect(report.message).toContain('generatedAt');
    expect(report.message).not.toContain('42');
  });
});
