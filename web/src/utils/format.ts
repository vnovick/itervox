/** Label shown in profile selects when no agent profile is assigned. */
export const EMPTY_PROFILE_LABEL = 'Default';

/**
 * Stable empty array for available-profiles fallback.
 * Re-exported from constants for backward compatibility.
 */
export { EMPTY_PROFILES } from './constants';

/** Format elapsed milliseconds as "Xs" or "Xm YYs". */
export function fmtMs(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${String(s)}s`;
  return `${String(Math.floor(s / 60))}m ${String(s % 60).padStart(2, '0')}s`;
}

/** Tailwind classes for the orchestrator state indicator dot. */
export function orchDotClass(state: string): string {
  if (state === 'running') return 'bg-green-500 animate-pulse';
  if (state === 'retrying') return 'bg-yellow-400 animate-pulse';
  if (state === 'paused') return 'bg-red-400';
  if (state === 'pending_input_resume') return 'bg-orange-400 animate-pulse';
  return 'bg-gray-300 dark:bg-gray-600';
}

export function formatOrchestratorState(state: string): string {
  if (state === 'pending_input_resume') return 'reply received';
  return state.replace(/_/g, ' ');
}

/** Tailwind classes for the priority indicator dot. Returns null when no priority. */
export function priorityDotClass(p: number | null | undefined): string | null {
  if (!p) return null; // null, undefined, and 0 are all "no priority"
  if (p === 1) return 'bg-red-500';
  if (p === 2) return 'bg-orange-400';
  if (p === 3) return 'bg-yellow-400';
  return 'bg-gray-400';
}

/**
 * Shared Tailwind prose classes for markdown content rendered inside the app.
 * Requires @tailwindcss/typography.
 */
export const proseClass = [
  'prose prose-sm dark:prose-invert max-w-none',
  'text-gray-800 dark:text-gray-200',
  'prose-p:my-1 prose-p:leading-relaxed',
  'prose-headings:font-semibold prose-headings:mt-3 prose-headings:mb-1',
  'prose-code:text-xs prose-code:bg-gray-100 dark:prose-code:bg-gray-800 prose-code:px-1 prose-code:rounded',
  'prose-code:text-gray-800 dark:prose-code:text-gray-200 prose-code:before:content-none prose-code:after:content-none',
  'prose-pre:bg-gray-100 dark:prose-pre:bg-gray-800 prose-pre:p-3 prose-pre:rounded-lg prose-pre:text-xs',
  'prose-ul:my-1 prose-ol:my-1 prose-li:my-0.5',
  'prose-blockquote:border-l-2 prose-blockquote:border-gray-300 dark:prose-blockquote:border-gray-600 prose-blockquote:pl-3 prose-blockquote:text-gray-500 dark:prose-blockquote:text-gray-400',
  'prose-a:text-blue-600 dark:prose-a:text-blue-400',
  'prose-strong:text-gray-900 dark:prose-strong:text-white',
].join(' ');

export type BadgeColor = 'primary' | 'success' | 'error' | 'warning' | 'info' | 'light' | 'dark';

/** Map a tracker state string to a Badge color variant. */
export function stateBadgeColor(state: string): BadgeColor {
  const s = state.toLowerCase();
  if (s.includes('progress')) return 'warning';
  if (s.includes('review') || s.includes('done')) return 'success';
  if (s.includes('todo')) return 'primary';
  return 'light';
}
