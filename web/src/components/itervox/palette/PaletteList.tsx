import { Fragment, useCallback, useEffect, useRef, useState } from 'react';
import { cn } from '../../ui/cn';
import { GROUP_LABEL, type Command } from './paletteModel';

/**
 * The palette's listbox: rows grouped under headers (a header wherever the
 * group changes), the active row highlighted, each row with its tag, label,
 * secondary detail and the shortcut that does the same.
 */
export function PaletteList({
  listId,
  rows,
  activeIndex,
  optionId,
  onHover,
  onPick,
}: {
  listId: string;
  rows: readonly Command[];
  activeIndex: number;
  optionId: (i: number) => string;
  onHover: (i: number) => void;
  onPick: (c: Command) => void;
}) {
  // The list fades out at its bottom edge while more rows sit below it, so a
  // half-visible last row reads as "scroll for more" rather than cut off.
  const listRef = useRef<HTMLUListElement>(null);
  const [fadeEnd, setFadeEnd] = useState(false);
  const updateFade = useCallback(() => {
    const el = listRef.current;
    setFadeEnd(!!el && el.scrollTop + el.clientHeight < el.scrollHeight - 1);
  }, []);
  useEffect(() => {
    // Measured after layout (the list's rows have rendered).
    const raf = requestAnimationFrame(updateFade);
    return () => {
      cancelAnimationFrame(raf);
    };
  }, [rows.length, updateFade]);

  // Keep the active row in view while arrowing through a long list.
  useEffect(() => {
    const el: Partial<Pick<Element, 'scrollIntoView'>> | null = document.getElementById(
      optionId(activeIndex),
    );
    el?.scrollIntoView?.({ block: 'nearest' });
  }, [activeIndex, optionId]);

  return (
    <ul
      ref={listRef}
      id={listId}
      role="listbox"
      aria-label="Commands"
      onScroll={updateFade}
      data-fade={fadeEnd || undefined}
      className={cn('max-h-[420px] overflow-y-auto px-2 py-2', fadeEnd && 'scroll-fade-end')}
    >
      {rows.length === 0 && (
        <li role="presentation" className="text-theme-muted px-3 py-2 text-xs">
          No matching commands
        </li>
      )}
      {rows.map((c, i) => {
        const active = i === activeIndex;
        return (
          <Fragment key={`${c.group}:${c.id}`}>
            {(i === 0 || rows[i - 1].group !== c.group) && (
              <li role="presentation" aria-hidden="true" className="eyebrow px-3 pt-2.5 pb-1.5">
                {GROUP_LABEL[c.group]}
              </li>
            )}
            {/* Options are picked with the keyboard via the combobox (arrow
                keys + Enter); the click is the mouse path to the same action. */}
            {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events -- keyboard handled on the combobox (aria-activedescendant pattern) */}
            <li
              id={optionId(i)}
              role="option"
              aria-selected={active}
              onMouseEnter={() => {
                onHover(i);
              }}
              onClick={() => {
                onPick(c);
              }}
              className={cn(
                'flex min-h-11 cursor-pointer items-center gap-3 rounded-[var(--radius-sm)] px-3 text-sm',
                active
                  ? 'bg-theme-accent-soft text-theme-accent-strong font-medium'
                  : c.danger
                    ? 'text-theme-danger-text'
                    : 'text-theme-text',
              )}
            >
              {c.tag && (
                <span
                  className={cn(
                    'flex-shrink-0 font-mono text-xs',
                    active ? 'font-semibold' : 'text-theme-text-secondary',
                  )}
                >
                  {c.tag}
                </span>
              )}
              <span className="min-w-0 flex-1 truncate">{c.label}</span>
              {c.detail && (
                <span
                  className={cn(
                    'max-w-[40%] flex-shrink-0 truncate text-xs',
                    active ? 'font-normal' : 'text-theme-muted',
                  )}
                >
                  {c.detail}
                </span>
              )}
              {c.hint && (
                <kbd className="text-theme-text-secondary flex-shrink-0 font-mono text-[11px]">
                  {c.hint}
                </kbd>
              )}
              {active && !c.hint && (
                <kbd aria-hidden="true" className="flex-shrink-0 font-mono text-[11px]">
                  ↵
                </kbd>
              )}
            </li>
          </Fragment>
        );
      })}
    </ul>
  );
}
