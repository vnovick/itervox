import type { DiffSide, LineTarget } from '../../../store/reviewDraftStore';
import type { DiffLine } from './diffParse';

// #238 — a selected line or range of a diff, as a review-comment target.

/** The selected rows: patch positions (DiffLine.n), anchor first. */
export interface LineSelection {
  anchor: number;
  focus: number;
}

/** Only diff content lines (not headers or hunk markers) take comments. */
export function isCommentable(l: DiffLine): boolean {
  return l.kind === 'add' || l.kind === 'del' || l.kind === 'ctx';
}

/**
 * The next selection after a click on row `n`: a plain click selects one
 * line; a shift-click extends the selection within the same hunk (GitHub
 * anchors a range comment to one hunk), else starts over.
 */
export function nextSelection(
  lines: readonly DiffLine[],
  prev: LineSelection | null,
  n: number,
  extend: boolean,
): LineSelection {
  if (extend && prev && lines[prev.anchor]?.hunk === lines[n]?.hunk) {
    return { anchor: prev.anchor, focus: n };
  }
  return { anchor: n, focus: n };
}

/** Whether row `n` is inside `sel`. */
export function inSelection(sel: LineSelection | null, n: number): boolean {
  if (!sel) return false;
  return n >= Math.min(sel.anchor, sel.focus) && n <= Math.max(sel.anchor, sel.focus);
}

function lineRef(l: DiffLine): { line: number; side: DiffSide } {
  return l.kind === 'del'
    ? { line: l.oldLine ?? 0, side: 'LEFT' }
    : { line: l.newLine ?? 0, side: 'RIGHT' };
}

/**
 * The comment target of `sel` in `path`, and the selected lines' text (the
 * quote an agent message carries). A deleted line anchors to the base file
 * (LEFT), any other to the new file (RIGHT). null when nothing commentable
 * is selected.
 */
export function selectionTarget(
  lines: readonly DiffLine[],
  sel: LineSelection,
  path: string,
): { target: LineTarget; quote: string } | null {
  const from = Math.min(sel.anchor, sel.focus);
  const to = Math.max(sel.anchor, sel.focus);
  const rows = lines.slice(from, to + 1).filter(isCommentable);
  if (rows.length === 0) return null;
  const first = lineRef(rows[0]);
  const last = lineRef(rows[rows.length - 1]);
  const target: LineTarget = { path, line: last.line, side: last.side };
  if (rows.length > 1 && (first.line !== last.line || first.side !== last.side)) {
    target.startLine = first.line;
    target.startSide = first.side;
  }
  return { target, quote: rows.map((r) => r.text).join('\n') };
}

/** "line 12", "lines 12–14", with "(base)" for the base side. */
export function describeTarget(t: LineTarget): string {
  const base = (s?: DiffSide) => (s === 'LEFT' ? ' (base)' : '');
  if (t.startLine === undefined) return `line ${String(t.line)}${base(t.side)}`;
  if (t.startSide === t.side) {
    return `lines ${String(t.startLine)}–${String(t.line)}${base(t.side)}`;
  }
  return `lines ${String(t.startLine)}${base(t.startSide)}–${String(t.line)}${base(t.side)}`;
}
