/**
 * Shared timeline styling constants and utilities.
 * Replaces inline styles with reusable functions and Tailwind-compatible values.
 */

import type { NormalisedSession } from './types';

// ─── Layout constants ────────────────────────────────────────────────────────

/** Height of the main run bar in pixels. */
export const BAR_HEIGHT = 24;

/** Height of the subagent/expanded child bar in pixels. */
export const SUB_BAR_HEIGHT = 16;

/** Minimum row height for a run entry. */
export const ROW_MIN_HEIGHT = 44;

/** Width of the run number + time label column. */
export const LABEL_WIDTH = 120;

/** Width of the elapsed time column. */
export const ELAPSED_WIDTH = 60;

/** Width of the per-run token-count column. */
export const TOKENS_WIDTH = 60;

/** Width of the subagent name label. */
export const SUBAGENT_LABEL_WIDTH = 80;

/** Left margin for the time axis (aligns with bar area). */
export const AXIS_MARGIN_LEFT = 140;

/** Right margin for the time axis (aligns with elapsed/status). */
export const AXIS_MARGIN_RIGHT = 80;

// ─── Status bar gradients ────────────────────────────────────────────────────

type SessionStatus = NormalisedSession['status'];

const BAR_GRADIENTS: Record<SessionStatus, string> = {
  live: 'linear-gradient(90deg, var(--accent), var(--teal))',
  succeeded: 'linear-gradient(90deg, var(--success), #16a34a)',
  failed: 'linear-gradient(90deg, var(--danger), #dc2626)',
  cancelled: 'linear-gradient(90deg, #52525b, #3f3f46)',
  stalled: 'linear-gradient(90deg, var(--warning, #f59e0b), #d97706)',
  input_required: 'linear-gradient(90deg, var(--warning, #f59e0b), #d97706)',
};

/** Returns the CSS gradient background for a run bar based on status. */
export function barGradient(status: SessionStatus): string {
  return BAR_GRADIENTS[status];
}

// ─── Status badge styling ────────────────────────────────────────────────────

interface BadgeStyle {
  background: string;
  color: string;
}

const BADGE_STYLES: Record<string, BadgeStyle> = {
  succeeded: { background: 'var(--success-soft)', color: 'var(--success-strong)' },
  failed: { background: 'var(--danger-soft)', color: 'var(--danger)' },
  stalled: { background: 'rgba(245,158,11,0.15)', color: 'var(--warning, #f59e0b)' },
  input_required: { background: 'rgba(245,158,11,0.15)', color: 'var(--warning, #f59e0b)' },
};

const DEFAULT_BADGE: BadgeStyle = {
  background: 'rgba(113,113,122,0.15)',
  color: 'var(--text-secondary)',
};

/** Returns background + color for a status badge. */
export function statusBadgeStyle(status: SessionStatus): BadgeStyle {
  return BADGE_STYLES[status] ?? DEFAULT_BADGE;
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
  { text: 'var(--accent-strong)', bar: 'linear-gradient(90deg, #a855f7, #6366f1)' },
  { text: 'var(--teal)', bar: 'linear-gradient(90deg, #14b8a6, #22c55e)' },
  { text: 'var(--accent-strong)', bar: 'linear-gradient(90deg, var(--accent), var(--teal))' },
] as const;

/** Returns subagent color pair by cycling index. */
export function subagentColor(idx: number) {
  return SUBAGENT_COLORS[idx % SUBAGENT_COLORS.length];
}

// ─── Tick mark styling ───────────────────────────────────────────────────────

/** CSS for a tick mark (subagent boundary or action event). */
export const TICK_MARK_STYLE = 'rgba(255,255,255,0.4)';
