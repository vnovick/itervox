import { describe, expect, it } from 'vitest';
import { render } from '@testing-library/react';
import { TimeAxis } from '../TimeAxis';
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

  it('keeps multi-day axis labels date-only even if a sub-day step is supplied', () => {
    const ms = new Date(2026, 4, 1, 3, 0).getTime();
    const span = 2 * 24 * 60 * 60_000;

    expect(formatTimeAxisLabel(ms, span, 6 * 60 * 60_000)).toBe('05-01');
  });

  it('uses local-day date-only ticks for multi-day spans', () => {
    const start = new Date(2026, 4, 1, 0, 3).getTime();
    const end = new Date(2026, 4, 4, 0, 3).getTime();
    const ticks = buildTimeAxisTicks(start, end);

    expect(timeAxisStep(end - start)).toBe(24 * 60 * 60_000);
    expect(ticks).toHaveLength(4);
    expect(ticks[0]).toBe(start);
    expect(ticks.slice(1).every((tick) => new Date(tick).getHours() === 0)).toBe(true);
    expect(ticks.map((tick) => formatTimeAxisLabel(tick, end - start))).toEqual([
      '05-01',
      '05-02',
      '05-03',
      '05-04',
    ]);
  });

  it('uses day-scale labels for near-two-day spans instead of dense date-time labels', () => {
    const start = new Date(2026, 4, 1, 3, 0).getTime();
    const end = new Date(2026, 4, 3, 2, 55).getTime();
    const span = end - start;
    const ticks = buildTimeAxisTicks(start, end);

    expect(timeAxisStep(span)).toBe(24 * 60 * 60_000);
    expect(ticks.length).toBeLessThanOrEqual(3);
    expect(ticks[0]).toBe(start);
    expect(ticks.slice(1).every((tick) => new Date(tick).getHours() === 0)).toBe(true);
    expect(ticks.map((tick) => formatTimeAxisLabel(tick, span))).toEqual([
      '05-01',
      '05-02',
      '05-03',
    ]);
    expect(ticks.map((tick) => formatTimeAxisLabel(tick, span)).join('')).not.toContain(':');
  });

  it('switches to date-only calendar-day ticks once the viewport spans a day', () => {
    const start = new Date(2026, 4, 1, 3, 0).getTime();
    const end = start + 30 * 60 * 60_000;
    const span = end - start;
    const ticks = buildTimeAxisTicks(start, end);

    expect(timeAxisStep(span)).toBe(24 * 60 * 60_000);
    expect(ticks.length).toBeGreaterThanOrEqual(1);
    expect(ticks[0]).toBe(start);
    expect(ticks.slice(1).every((tick) => new Date(tick).getHours() === 0)).toBe(true);
    expect(ticks.map((tick) => formatTimeAxisLabel(tick, span))).toEqual(['05-01', '05-02']);
    expect(ticks.map((tick) => formatTimeAxisLabel(tick, span)).join('')).not.toContain(':');
  });

  it('renders day-scale labels with readable separation', () => {
    const start = new Date(2026, 4, 1, 0, 3).getTime();
    const end = new Date(2026, 4, 4, 0, 3).getTime();

    const { container } = render(<TimeAxis viewStart={start} viewEnd={end} />);
    const axis = container.firstElementChild;

    expect(axis).not.toBeNull();
    expect(axis?.textContent).toBe('05-01 05-02 05-03 05-04');
    expect(axis?.textContent).not.toContain(':');
  });

  it('keeps very long spans bounded instead of falling back to hourly ticks', () => {
    const start = new Date(2026, 0, 1, 20, 0).getTime();
    const end = start + 90 * 24 * 60 * 60_000;

    expect(buildTimeAxisTicks(start, end).length).toBeLessThanOrEqual(8);
  });
});
