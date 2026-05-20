import { formatAxisTime } from './timeFormatting';

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

const TIME_STEPS = [
  30_000,
  MINUTE,
  5 * MINUTE,
  10 * MINUTE,
  15 * MINUTE,
  30 * MINUTE,
  HOUR,
  2 * HOUR,
  4 * HOUR,
  6 * HOUR,
  12 * HOUR,
  DAY,
  2 * DAY,
  7 * DAY,
] as const;

export function timeAxisStep(spanMs: number): number {
  const rawStep = spanMs / 6;
  const predefined = TIME_STEPS.find((step) => step >= rawStep);
  if (predefined) return predefined;
  return Math.ceil(rawStep / (7 * DAY)) * 7 * DAY;
}

export function buildTimeAxisTicks(viewStart: number, viewEnd: number): number[] {
  const span = viewEnd - viewStart;
  if (span <= 0) return [];

  const step = timeAxisStep(span);
  const ticks: number[] = [];
  const first = Math.ceil(viewStart / step) * step;
  for (let t = first; t <= viewEnd; t += step) ticks.push(t);
  return ticks;
}

export function formatTimeAxisLabel(
  ms: number,
  spanMs: number,
  stepMs = timeAxisStep(spanMs),
): string {
  if (spanMs < DAY) return formatAxisTime(ms);

  const d = new Date(ms);
  const month = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  if (stepMs >= DAY) return `${month}-${day}`;
  return `${month}-${day} ${formatAxisTime(ms)}`;
}
