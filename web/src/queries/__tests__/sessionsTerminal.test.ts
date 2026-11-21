import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fetchCallerScope } from '../sessionsTerminal';

vi.mock('../../auth/authedFetch', () => ({
  authedFetch: vi.fn(),
}));

import { authedFetch } from '../../auth/authedFetch';

const mockAuthedFetch = vi.mocked(authedFetch);

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

beforeEach(() => {
  mockAuthedFetch.mockReset();
});

// #298 follow-up — the caller's scope read fresh from GET /api/v1/state
// (the snapshot stream's scope is the one it connected with).
describe('fetchCallerScope', () => {
  it('reads the scope from a fresh state request', async () => {
    mockAuthedFetch.mockResolvedValueOnce(json({ callerScope: 'operator' }));
    await expect(fetchCallerScope()).resolves.toBe('operator');
    expect(mockAuthedFetch.mock.calls[0][0]).toBe('/api/v1/state');
  });

  it('treats an absent scope as admin (a daemon predating scopes)', async () => {
    mockAuthedFetch.mockResolvedValueOnce(json({}));
    await expect(fetchCallerScope()).resolves.toBe('admin');
  });

  it('treats an unknown scope as viewer', async () => {
    mockAuthedFetch.mockResolvedValueOnce(json({ callerScope: 7 }));
    await expect(fetchCallerScope()).resolves.toBe('viewer');
    mockAuthedFetch.mockResolvedValueOnce(json({ callerScope: 'root' }));
    await expect(fetchCallerScope()).resolves.toBe('viewer');
  });

  it('throws on a failed request', async () => {
    mockAuthedFetch.mockResolvedValueOnce(json({}, 500));
    await expect(fetchCallerScope()).rejects.toThrow();
  });
});
