import { describe, expect, it } from 'vitest';
import { formatAxisTime } from '../TimeAxis';

describe('formatAxisTime', () => {
  it('formats local clock labels as stable HH:mm values', () => {
    const ms = new Date(2026, 0, 1, 9, 5, 42).getTime();
    expect(formatAxisTime(ms)).toBe('09:05');
  });
});
