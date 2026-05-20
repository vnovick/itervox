import { describe, expect, it } from 'vitest';
import { buildTimeAxisTicks, formatTimeAxisLabel, timeAxisStep } from '../timeAxisModel';
import { formatAxisTime } from '../timeFormatting';

describe('formatAxisTime', () => {
  it('formats local clock labels as stable HH:mm values', () => {
    const ms = new Date(2026, 0, 1, 9, 5, 42).getTime();
    expect(formatAxisTime(ms)).toBe('09:05');
  });

  it('uses sparse day-scale ticks for long timeline spans', () => {
    const start = new Date(2026, 0, 1, 20, 0).getTime();
    const end = start + 7 * 24 * 60 * 60_000;

    expect(timeAxisStep(end - start)).toBe(2 * 24 * 60 * 60_000);
    expect(buildTimeAxisTicks(start, end).length).toBeLessThanOrEqual(5);
  });

  it('uses compact date-only labels for day-scale ticks', () => {
    const ms = new Date(2026, 0, 2, 0, 0).getTime();
    const span = 7 * 24 * 60 * 60_000;

    expect(formatTimeAxisLabel(ms, span, 24 * 60 * 60_000)).toBe('01-02');
  });

  it('keeps time in multi-day labels when ticks are sub-day', () => {
    const ms = new Date(2026, 0, 2, 6, 30).getTime();
    const span = 30 * 60 * 60_000;

    expect(formatTimeAxisLabel(ms, span, 6 * 60 * 60_000)).toBe('01-02 06:30');
  });

  it('keeps very long spans bounded instead of falling back to hourly ticks', () => {
    const start = new Date(2026, 0, 1, 20, 0).getTime();
    const end = start + 90 * 24 * 60 * 60_000;

    expect(buildTimeAxisTicks(start, end).length).toBeLessThanOrEqual(8);
  });
});
