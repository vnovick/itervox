/**
 * Shared timeline styling constants and utilities.
 * Tailwind class recipes and layout constants, so colours come from tokens.
 */

import type { NormalisedSession } from './types';

// ─── Layout constants ────────────────────────────────────────────────────────

/** Height of the main run bar in pixels. */
export const BAR_HEIGHT = 24;

/** Height of the subagent/expanded child bar in pixels. */
export const SUB_BAR_HEIGHT = 16;

/** Minimum row height for a run entry. */
export const ROW_MIN_HEIGHT = 44;

// ─── Shared row grid ─────────────────────────────────────────────────────────
// Every row (the time axis, run rows, the expanded main/subagent rows) uses
// the same columns, so a bar's left/width percentages land on one time axis.
// The grid switches on the width of the run list (`@container` on its
// wrapper), not the viewport: below ~36rem the bar track drops onto its own
// full-width line under the label and metadata, and the axis spans it too.
//   wide:   [chevron 16] [label 120] [track 1fr] [meta 200]   gap 8
//   narrow: [chevron 16] [label 1fr] [meta auto] / [track spans all]

/** Grid columns shared by every timeline row. */
export const ROW_GRID =
  'grid grid-cols-[16px_minmax(0,1fr)_auto] items-center gap-x-2 gap-y-1.5 @xl:grid-cols-[16px_120px_minmax(0,1fr)_200px]';

/** Places the bar track: its own line when narrow, the third column when wide. */
export const ROW_TRACK = 'col-span-3 row-start-2 @xl:col-span-1 @xl:col-start-3 @xl:row-start-1';

/** Places the right-hand metadata (elapsed, tokens, status). */
export const ROW_META =
  'col-start-3 row-start-1 flex items-center justify-end gap-2 @xl:col-start-4';

/** Axis insets that line the tick labels up with the wide-layout track
 *  (16 + 8 + 120 + 8 on the left, 8 + 200 on the right). */
export const AXIS_INSET = '@xl:ml-[152px] @xl:mr-[208px]';

// ─── Status bar fills ────────────────────────────────────────────────────────
// Flat, muted fills: a finished run reads as quiet grey, so the eye lands on
// the live, waiting and failed runs.

type SessionStatus = NormalisedSession['status'];

const BAR_FILLS: Record<SessionStatus, string> = {
  live: 'bg-theme-accent/80',
  succeeded: 'bg-theme-dot-idle/85',
  failed: 'bg-theme-danger/75',
  cancelled: 'bg-theme-dot-idle/50',
  stalled: 'bg-theme-dot-warn/75',
  input_required: 'bg-theme-dot-warn/75',
};

/** Returns the fill class for a run bar based on status. */
export function barFillClass(status: SessionStatus): string {
  return BAR_FILLS[status];
}

// ─── Status badge styling ────────────────────────────────────────────────────

const BADGE_TONES: Partial<Record<SessionStatus, string>> = {
  succeeded: 'pill-ok',
  failed: 'pill-danger',
  stalled: 'pill-warn',
  input_required: 'pill-warn',
};

/** Returns the `.tag` tone class for a status badge. */
export function statusBadgeTone(status: SessionStatus): string {
  return BADGE_TONES[status] ?? 'pill-neutral';
}

/** Returns the display label for a status badge. */
export function statusBadgeLabel(status: SessionStatus): string {
  switch (status) {
    case 'succeeded':
      return 'done';
    case 'failed':
      return 'failed';
    case 'stalled':
      return 'stalled';
    case 'input_required':
      return 'input';
    case 'cancelled':
      return 'cancelled';
    default:
      return status;
  }
}

// ─── Subagent bar colours (cycling) ──────────────────────────────────────────

export const SUBAGENT_COLORS = [
  { text: 'text-theme-purple-text', bar: 'bg-theme-purple/60' },
  { text: 'text-theme-accent-text', bar: 'bg-theme-teal/60' },
  { text: 'text-theme-accent-text', bar: 'bg-theme-accent/60' },
] as const;

/** Returns subagent text/bar classes by cycling index. */
export function subagentColor(idx: number) {
  return SUBAGENT_COLORS[idx % SUBAGENT_COLORS.length];
}
