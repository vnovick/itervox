import { describe, expect, it } from 'vitest';
import { isFutureInstant } from '../outboxListModel';

describe('isFutureInstant', () => {
  const now = Date.parse('2026-09-16T12:00:00Z');

  it('is true for an instant after now', () => {
    expect(isFutureInstant('2026-09-16T12:00:01Z', now)).toBe(true);
  });

  it('is false for an instant before now', () => {
    expect(isFutureInstant('2026-09-16T11:59:59Z', now)).toBe(false);
  });

  it('is false for exactly now', () => {
    expect(isFutureInstant('2026-09-16T12:00:00Z', now)).toBe(false);
  });

  it('is false for an absent value', () => {
    expect(isFutureInstant(undefined, now)).toBe(false);
    expect(isFutureInstant('', now)).toBe(false);
  });

  it('is false for an unparseable value', () => {
    expect(isFutureInstant('not-a-date', now)).toBe(false);
  });
});
