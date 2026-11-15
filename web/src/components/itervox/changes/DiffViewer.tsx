import { useEffect, useMemo, useRef, type ReactNode } from 'react';
import { parseUnifiedDiff, splitRows, type DiffLine } from './diffParse';
import { inSelection, isCommentable, type LineSelection } from './lineSelection';
import type { DiffLayout } from '../../../store/diffPrefsStore';

const LINE_TONE: Record<DiffLine['kind'], string> = {
  meta: 'text-theme-muted',
  hunk: 'text-theme-accent-text bg-theme-bg-soft',
  add: 'text-theme-success-text bg-theme-success-soft',
  del: 'text-theme-danger-text bg-theme-danger-soft',
  ctx: 'text-theme-text',
};

/** A draft comment shown under the row it ends on. */
export interface RowNote {
  id: string;
  body: string;
}

type LineClick = (n: number, extend: boolean) => void;

/**
 * #215 — a diff, one row per line (unified) or old and new side by side
 * (split). The patch is rendered as text nodes only (React escapes them);
 * it never goes through innerHTML, so a diff that contains markup stays
 * text.
 *
 * #238 / review and continue — with `onLineClick`, each content line gets a
 * line-number button that selects it: shift-click or a drag across line
 * numbers extends the selection to a range. Draft comments (`notes`, keyed
 * by row) show under the row they end on.
 */
export function DiffViewer({
  patch,
  lines,
  selection = null,
  onLineClick,
  layout = 'unified',
  notes,
}: {
  patch: string;
  /** The parsed patch, when the caller already has it. */
  lines?: readonly DiffLine[];
  selection?: LineSelection | null;
  onLineClick?: LineClick;
  layout?: DiffLayout;
  notes?: ReadonlyMap<number, readonly RowNote[]>;
}) {
  const rows = useMemo(() => lines ?? parseUnifiedDiff(patch), [lines, patch]);
  const dragging = useRef(false);
  useEffect(() => {
    const stop = () => {
      dragging.current = false;
    };
    window.addEventListener('mouseup', stop);
    return () => {
      window.removeEventListener('mouseup', stop);
    };
  }, []);

  const selected = (l: DiffLine | undefined) => Boolean(l && inSelection(selection, l.n));

  const numberCell = (l: DiffLine | undefined, side: 'old' | 'new' | 'any'): ReactNode => {
    if (!onLineClick) return null;
    let num: number | undefined;
    if (l) num = side === 'old' ? l.oldLine : side === 'new' ? l.newLine : (l.newLine ?? l.oldLine);
    // A context line in the split view's left column is shown, not
    // selectable: its target is the new file (the right column's button).
    const selectable =
      l !== undefined &&
      isCommentable(l) &&
      num !== undefined &&
      !(side === 'old' && l.kind === 'ctx');
    return (
      <span className="text-theme-muted w-10 shrink-0 pr-1 text-right select-none">
        {selectable ? (
          <button
            type="button"
            aria-label={`Select ${l.kind === 'del' ? 'base ' : ''}line ${String(num)}`}
            aria-pressed={selected(l)}
            className="hover:text-theme-accent-text w-full cursor-pointer text-right"
            onMouseDown={(e) => {
              if (e.button !== 0) return;
              e.preventDefault(); // no text selection while dragging
              dragging.current = true;
              onLineClick(l.n, e.shiftKey);
            }}
            onMouseEnter={(e) => {
              if (dragging.current && (e.buttons & 1) === 1) onLineClick(l.n, true);
            }}
            onClick={(e) => {
              // A mouse click was handled on mousedown; detail 0 is the keyboard.
              if (e.detail === 0) onLineClick(l.n, e.shiftKey);
            }}
          >
            {num}
          </button>
        ) : (
          num
        )}
      </span>
    );
  };

  const text = (l: DiffLine | undefined) => (
    <span className={`block flex-1 px-2 ${l ? LINE_TONE[l.kind] : 'bg-theme-bg-soft'}`}>
      {l ? (l.text === '' ? ' ' : l.text) : ' '}
    </span>
  );

  const noteRows = (n: number) =>
    notes?.get(n)?.map((c) => (
      <span
        key={c.id}
        id={`draft-comment-${c.id}`}
        data-testid="diff-draft-comment"
        className="border-theme-accent bg-theme-accent-soft text-theme-text my-0.5 ml-10 block border-l-2 px-2 py-1 font-sans text-xs whitespace-pre-wrap"
      >
        {c.body}
      </span>
    ));

  const ring = (l: DiffLine | undefined) =>
    selected(l) ? 'ring-theme-accent ring-1 ring-inset' : '';

  return (
    <pre
      className="border-theme-line max-h-[60vh] overflow-auto rounded-[var(--radius-sm)] border text-[11px] leading-[1.45]"
      data-testid="diff-viewer"
      data-layout={layout}
    >
      <code className="block min-w-max">
        {layout === 'split'
          ? splitRows(rows).map((r) => {
              if (r.kind === 'full') {
                return (
                  <span key={`${String(r.line.n)}:${r.line.kind}`} className="flex">
                    {onLineClick && <span className="w-10 shrink-0" />}
                    {text(r.line)}
                  </span>
                );
              }
              const ends = [
                ...new Set([r.left?.n, r.right?.n].filter((n): n is number => n !== undefined)),
              ];
              return (
                <span key={ends.join('|')} className="block">
                  <span className="grid grid-cols-2">
                    <span
                      className={`flex ${ring(r.left)}`}
                      data-selected={selected(r.left) || undefined}
                    >
                      {numberCell(r.left, 'old')}
                      {text(r.left)}
                    </span>
                    <span
                      className={`border-theme-line flex border-l ${ring(r.right)}`}
                      data-selected={selected(r.right) || undefined}
                    >
                      {numberCell(r.right, 'new')}
                      {text(r.right)}
                    </span>
                  </span>
                  {ends.map((n) => noteRows(n))}
                </span>
              );
            })
          : rows.map((l) => (
              <span key={`${String(l.n)}:${l.kind}`} className="block">
                <span className={`flex ${ring(l)}`} data-selected={selected(l) || undefined}>
                  {numberCell(l, 'any')}
                  {text(l)}
                </span>
                {noteRows(l.n)}
              </span>
            ))}
      </code>
    </pre>
  );
}
