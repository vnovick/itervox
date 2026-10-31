import { formatAxisTime } from './timeFormatting';

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const DAY_SCALE_SPAN = DAY;
const MIN_DAY_EDGE_GAP = 4 * HOUR;

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

/** Ticks the axis aims for when its width is unknown or wide enough. */
export const DEFAULT_TICK_TARGET = 6;
/** Horizontal room one tick label needs (an `HH:mm` label plus breathing space). */
export const MIN_TICK_SPACING_PX = 56;

/** How many ticks fit an axis `widthPx` wide (at least 2, at most the default). */
export function tickTargetForWidth(widthPx: number): number {
  if (!(widthPx > 0)) return DEFAULT_TICK_TARGET;
  return Math.max(2, Math.min(DEFAULT_TICK_TARGET, Math.floor(widthPx / MIN_TICK_SPACING_PX)));
}

export function timeAxisStep(spanMs: number, targetTicks: number = DEFAULT_TICK_TARGET): number {
  const rawStep = spanMs / Math.max(1, targetTicks);
  if (spanMs >= DAY_SCALE_SPAN && rawStep < DAY) return DAY;
  const predefined = TIME_STEPS.find((step) => step >= rawStep);
  if (predefined) return predefined;
  return Math.ceil(rawStep / (7 * DAY)) * 7 * DAY;
}

function startOfLocalDay(ms: number): number {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

function addLocalDays(ms: number, days: number): number {
  const d = new Date(ms);
  d.setDate(d.getDate() + days);
  return d.getTime();
}

export function buildTimeAxisTicks(
  viewStart: number,
  viewEnd: number,
  targetTicks: number = DEFAULT_TICK_TARGET,
): number[] {
  const span = viewEnd - viewStart;
  if (span <= 0) return [];

  const step = timeAxisStep(span, targetTicks);
  const ticks: number[] = [];
  if (step >= DAY) {
    const stepDays = Math.max(1, Math.round(step / DAY));
    ticks.push(viewStart);
    let t = startOfLocalDay(viewStart);
    if (t <= viewStart) t = addLocalDays(t, stepDays);
    for (; t <= viewEnd; t = addLocalDays(t, stepDays)) ticks.push(t);
    return ticks.filter((tick, idx) => idx === 0 || tick - ticks[idx - 1] >= MIN_DAY_EDGE_GAP);
  }

  const first = Math.ceil(viewStart / step) * step;
  for (let t = first; t <= viewEnd; t += step) ticks.push(t);
  return ticks;
}

export function formatTimeAxisLabel(ms: number, spanMs: number, stepMs?: number): string {
  // Keep the optional step parameter for older call sites/tests; day-scale labels are now date-only.
  void stepMs;
  if (spanMs < DAY) return formatAxisTime(ms);

  const d = new Date(ms);
  const month = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return `${month}-${day}`;
}
