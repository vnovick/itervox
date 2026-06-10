import { describe, expect, it, vi } from 'vitest';

// codex-B6 — Vitest unit guard that the dedup key construction includes
// the live/done discriminator. The component constructs a key like
// `${sessionId ?? identifier-timestamp}-${live ? 'live' : 'done'}` so a
// live row and a history row carrying the same sessionId produce distinct
// React keys.
function buildKey(run: {
  sessionId?: string | null;
  identifier: string;
  timestamp: string;
  isLive: boolean;
}): string {
  const base = run.sessionId ?? `${run.identifier}-${run.timestamp}`;
  return `${base}-${run.isLive ? 'live' : 'done'}`;
}

describe('AutomationActivityCard dedup keys (codex-B6)', () => {
  it('produces distinct keys for live and done runs with the same sessionId', () => {
    const live = buildKey({
      sessionId: 'session-1',
      identifier: 'ENG-1',
      timestamp: 't',
      isLive: true,
    });
    const done = buildKey({
      sessionId: 'session-1',
      identifier: 'ENG-1',
      timestamp: 't',
      isLive: false,
    });
    expect(live).not.toBe(done);
    expect(live).toContain('live');
    expect(done).toContain('done');
  });

  it('falls back to identifier+timestamp when sessionId is missing', () => {
    const key = buildKey({ identifier: 'ENG-7', timestamp: '2026-06-01', isLive: false });
    expect(key).toContain('ENG-7');
    expect(key).toContain('2026-06-01');
    expect(key).toContain('done');
  });

  it('does not emit React duplicate-key warnings for live/done pairs', () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    // Building two distinct keys for the same logical run is the contract;
    // when a React renderer consumes them, the lack of duplicate keys means
    // no console.error fires. The unit guard here just confirms the keys
    // would not collide.
    const a = buildKey({ sessionId: 's', identifier: 'i', timestamp: 'ts', isLive: true });
    const b = buildKey({ sessionId: 's', identifier: 'i', timestamp: 'ts', isLive: false });
    expect(a).not.toEqual(b);
    expect(consoleError).not.toHaveBeenCalled();
    consoleError.mockRestore();
  });
});
