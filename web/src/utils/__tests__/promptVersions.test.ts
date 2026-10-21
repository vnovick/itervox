import { describe, expect, it } from 'vitest';

import { HistoryRowSchema } from '../../types/schemas';
import type { HistoryRow } from '../../types/schemas';
import { groupByPromptVersion, shortDigest } from '../promptVersions';

const A = 'a'.repeat(64);
const B = 'b'.repeat(64);

function row(over: Partial<HistoryRow>): HistoryRow {
  return {
    identifier: 'ENG-1',
    startedAt: '2026-10-01T00:00:00Z',
    finishedAt: '2026-10-01T00:01:00Z',
    elapsedMs: 60_000,
    turnCount: 1,
    tokens: 0,
    inputTokens: 0,
    outputTokens: 0,
    status: 'succeeded',
    ...over,
  };
}

describe('groupByPromptVersion (#186)', () => {
  it('groups runs per digest with counts and success rate, newest version first', () => {
    const versions = groupByPromptVersion([
      row({ promptDigest: A, status: 'succeeded', startedAt: '2026-10-01T00:00:00Z' }),
      row({ promptDigest: A, status: 'failed', startedAt: '2026-10-02T00:00:00Z' }),
      row({ promptDigest: B, status: 'succeeded', startedAt: '2026-10-03T00:00:00Z' }),
      row({}), // recorded before #186 — no digest
    ]);
    expect(versions.map((v) => v.digest)).toEqual([B, A]);
    expect(versions[1]).toMatchObject({ runs: 2, succeeded: 1, successRate: 0.5 });
    expect(versions[0]).toMatchObject({ runs: 1, succeeded: 1, successRate: 1 });
  });

  it('is empty without digests and shortens to 12 characters', () => {
    expect(groupByPromptVersion([row({})])).toEqual([]);
    expect(shortDigest(A)).toBe('aaaaaaaaaaaa');
  });
});

describe('HistoryRowSchema run provenance (#186)', () => {
  it('parses promptDigest, parts, runId and steers, and still parses a legacy row', () => {
    const parsed = HistoryRowSchema.parse({
      ...row({}),
      runId: 'run-1',
      profileName: 'implementer',
      promptDigest: A,
      promptParts: { soul: '1111111111111111', rules: '2222222222222222' },
      steers: [{ text: 'use pnpm', at: '2026-10-01T00:00:30Z', delivery: 'live' }],
      steersDropped: 3,
    });
    expect(parsed.promptDigest).toBe(A);
    expect(parsed.promptParts?.soul).toBe('1111111111111111');
    expect(parsed.steers?.[0]).toEqual({
      text: 'use pnpm',
      at: '2026-10-01T00:00:30Z',
      delivery: 'live',
    });
    expect(parsed.steersDropped).toBe(3);

    const legacy = HistoryRowSchema.parse(row({}));
    expect(legacy.promptDigest).toBeUndefined();
    expect(legacy.steers).toBeUndefined();
  });

  it('tolerates an unknown delivery path', () => {
    const parsed = HistoryRowSchema.parse({
      ...row({}),
      steers: [{ text: 'x', at: '2026-10-01T00:00:30Z', delivery: 'carrier-pigeon' }],
    });
    expect(parsed.steers?.[0]?.delivery).toBe('resume');
  });
});
