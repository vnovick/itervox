import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { usePrefersReducedMotion } from '../../../hooks/usePrefersReducedMotion';

export type LogLevel = 'info' | 'action' | 'warn' | 'error' | 'subagent';

export interface LogEntry {
  ts: number;
  level: LogLevel;
  message: string;
  /**
   * Stable per-line id when the source has one (CORE-069): the global log
   * stream assigns a monotonic client id in the store. Used as the React key
   * and as the line's identity when counting unseen lines. Without it the
   * key falls back to ts+index and identity to level+message.
   */
  seq?: number;
}

interface TerminalProps {
  entries: LogEntry[];
  follow?: boolean;
  showTime?: boolean;
  className?: string;
  /**
   * Changing this (issue identifier, filter fingerprint) starts a new view:
   * the unseen count resets and the view follows the bottom again.
   */
  resetKey?: string;
}

const LEVEL_COLOR: Record<LogLevel, string> = {
  info: 'var(--text-secondary)',
  action: '#818cf8', // indigo
  warn: 'var(--warning-text)',
  error: 'var(--danger-text)',
  subagent: '#a855f7', // purple
};

// Within this many px of the bottom counts as "at the bottom" (sub-pixel
// rounding and a line that is still wrapping).
const AT_BOTTOM_SLACK_PX = 8;

function formatTime(ts: number) {
  return new Date(ts).toLocaleTimeString('en-GB', { hour12: false });
}

function identity(entry: LogEntry): string {
  return entry.seq !== undefined ? `#${String(entry.seq)}` : `${entry.level}\u0000${entry.message}`;
}

/**
 * Number of entries in `next` that were appended after `prev`'s last entry.
 * Works for capped windows (equal length, oldest lines dropped): the previous
 * last line is located by identity, not by length. When it is no longer in
 * the window every line is new.
 */
function countAppended(prev: readonly LogEntry[], next: readonly LogEntry[]): number {
  if (prev.length === 0) return next.length;
  const last = identity(prev[prev.length - 1]);
  const beforeLast = prev.length > 1 ? identity(prev[prev.length - 2]) : null;
  for (let j = next.length - 1; j >= 0; j--) {
    if (identity(next[j]) !== last) continue;
    // Disambiguate repeated lines by also matching the line before it.
    if (beforeLast !== null && j > 0 && identity(next[j - 1]) !== beforeLast) continue;
    return next.length - 1 - j;
  }
  return next.length;
}

/**
 * Log viewer. CORE-069: sticks to the bottom only while the reader is already
 * there; scrolled up, new lines are counted in an "N new" pill that jumps to
 * the latest line. Following is keyed on the last line's identity, so a
 * capped window that advances at equal length keeps following.
 */
/** Upper bound on a smooth jump-to-latest animation (BH-M5-7 fallback). */
const SMOOTH_JUMP_MAX_MS = 1500;

export function Terminal({
  entries,
  follow = true,
  showTime = false,
  className,
  resetKey,
}: TerminalProps) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const reducedMotion = usePrefersReducedMotion();
  const [atBottom, setAtBottom] = useState(true);
  const [unseen, setUnseen] = useState(0);
  const [prevEntries, setPrevEntries] = useState(entries);
  const [prevResetKey, setPrevResetKey] = useState(resetKey);

  // Derive the unseen count while rendering (React's "adjust state when a
  // prop changes" pattern) rather than in an effect, so the pill appears in
  // the same commit as the lines it counts.
  if (resetKey !== prevResetKey) {
    setPrevResetKey(resetKey);
    setPrevEntries(entries);
    setUnseen(0);
    setAtBottom(true);
  } else if (entries !== prevEntries) {
    setPrevEntries(entries);
    if (follow && !atBottom) setUnseen((n) => n + countAppended(prevEntries, entries));
  }

  const lastKey = entries.length > 0 ? identity(entries[entries.length - 1]) : '';

  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (follow && atBottom && el) el.scrollTop = el.scrollHeight;
  }, [follow, atBottom, lastKey, resetKey]);

  // BH-M5-7 — while a smooth jump to the latest line animates, the browser
  // fires scroll events at intermediate positions. They are not the operator
  // scrolling up, so they must not turn following off. The jump ends when the
  // view reaches the bottom, when the operator takes over (wheel / touch, or
  // any scroll that moves UP — the jump only ever moves down), or after a
  // fallback timeout.
  const jumpingRef = useRef(false);
  const lastTopRef = useRef(0);
  const jumpTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const endJump = () => {
    jumpingRef.current = false;
    if (jumpTimerRef.current !== null) clearTimeout(jumpTimerRef.current);
    jumpTimerRef.current = null;
  };
  useEffect(() => endJump, []);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight <= AT_BOTTOM_SLACK_PX;
    const movedUp = el.scrollTop < lastTopRef.current;
    lastTopRef.current = el.scrollTop;
    if (jumpingRef.current) {
      if (bottom || movedUp) endJump();
      if (!movedUp) return;
    }
    setAtBottom(bottom);
    if (bottom) setUnseen(0);
  };

  const jumpToLatest = () => {
    const el = scrollRef.current;
    if (!el) return;
    if (!reducedMotion) {
      endJump();
      jumpingRef.current = true;
      jumpTimerRef.current = setTimeout(endJump, SMOOTH_JUMP_MAX_MS);
    }
    if (typeof el.scrollTo === 'function') {
      el.scrollTo({ top: el.scrollHeight, behavior: reducedMotion ? 'auto' : 'smooth' });
    } else {
      el.scrollTop = el.scrollHeight;
    }
    setAtBottom(true);
    setUnseen(0);
  };

  const showPill = follow && !atBottom && unseen > 0;

  return (
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions -- scroll container: wheel/touch/key events only cancel a running "jump to latest" animation when the user takes over scrolling; not a control
    <div
      ref={scrollRef}
      data-testid="terminal-scroll"
      onScroll={onScroll}
      onWheel={endJump}
      onTouchStart={endJump}
      onKeyDown={endJump}
      className={['overflow-y-auto font-mono text-xs', className ?? ''].join(' ')}
      style={{ background: 'var(--bg)', color: 'var(--text-secondary)' }}
    >
      {entries.map((entry, i) => (
        <div
          key={entry.seq !== undefined ? entry.seq : `${String(entry.ts)}-${String(i)}`}
          data-seq={entry.seq}
          className="flex gap-2 px-2 py-0.5 leading-5"
        >
          {showTime && (
            // CORE-071: --muted at full opacity; text-secondary at opacity 0.4
            // composited to ~1.6:1 against --bg.
            <span
              data-testid={`terminal-time-${String(i)}`}
              className="shrink-0"
              style={{ color: 'var(--muted)' }}
            >
              {formatTime(entry.ts)}
            </span>
          )}
          <span
            data-level={entry.level}
            className="whitespace-pre-wrap"
            style={{ color: LEVEL_COLOR[entry.level] }}
          >
            {entry.message}
          </span>
        </div>
      ))}
      {showPill && (
        // Sticky inside the scroller, so it stays in view at the bottom edge
        // without a positioned wrapper around every caller's layout.
        <div className="pointer-events-none sticky bottom-2 flex justify-center">
          <button
            type="button"
            data-testid="terminal-new-pill"
            onClick={jumpToLatest}
            aria-label={`${String(unseen)} new log line${unseen === 1 ? '' : 's'}, jump to latest`}
            className="bg-theme-accent pointer-events-auto rounded-full px-3 py-1 font-sans text-[11px] font-semibold text-white shadow-md motion-safe:transition-opacity"
          >
            {unseen} new ↓
          </button>
        </div>
      )}
    </div>
  );
}
