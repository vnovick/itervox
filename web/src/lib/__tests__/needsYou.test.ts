import { describe, expect, it } from 'vitest';
import { formatWait, isDecision, waitOf } from '../needsYou';
import { NeedsYouRowSchema } from '../../types/schemas';

describe('needsYou model', () => {
  it('formats a wait in minutes, hours and days', () => {
    expect(formatWait(30_000)).toBe('under 1m');
    expect(formatWait(14 * 60_000 + 5_000)).toBe('14m');
    expect(formatWait((2 * 60 + 14) * 60_000)).toBe('2h 14m');
    expect(formatWait((3 * 24 + 4) * 3_600_000)).toBe('3d 4h');
    expect(formatWait(-1)).toBeUndefined();
    expect(formatWait(Number.NaN)).toBeUndefined();
  });

  it('reads the wait of a row, undefined when unknown', () => {
    const now = Date.parse('2026-10-22T12:00:00Z');
    expect(waitOf({ waitingSince: '2026-10-22T09:46:00Z' }, now)).toBe('2h 14m');
    expect(waitOf({}, now)).toBeUndefined();
    expect(waitOf({ waitingSince: 'not a time' }, now)).toBeUndefined();
  });

  it('decides approvals and pipeline changes, answers questions', () => {
    expect(isDecision('approval')).toBe(true);
    expect(isDecision('pipeline-change')).toBe(true);
    expect(isDecision('question')).toBe(false);
  });

  it('parses a row, tolerating an unknown kind from a newer daemon', () => {
    const row = NeedsYouRowSchema.parse({ kind: 'later-kind', identifier: 'ENG-1', summary: 'Q?' });
    expect(row.kind).toBe('question');
  });
});
