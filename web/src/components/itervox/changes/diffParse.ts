/** One rendered line of a unified diff. */
export interface DiffLine {
  kind: 'meta' | 'hunk' | 'add' | 'del' | 'ctx';
  text: string;
  /** Position in the patch; with the text it keys the row. */
  n: number;
  /** Which hunk the line belongs to (-1 before the first). */
  hunk: number;
  /** The line's number in the base file (del, ctx). */
  oldLine?: number;
  /** The line's number in the new file (add, ctx). */
  newLine?: number;
}

const HUNK_RE = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;

/** Classifies each line of a unified diff (git's patch format) and numbers it. */
export function parseUnifiedDiff(patch: string): DiffLine[] {
  const lines = patch.endsWith('\n') ? patch.slice(0, -1).split('\n') : patch.split('\n');
  let hunk = -1;
  let oldNo = 0;
  let newNo = 0;
  return lines.map((text, n): DiffLine => {
    if (text.startsWith('@@')) {
      hunk += 1;
      const m = HUNK_RE.exec(text);
      oldNo = m ? Number(m[1]) : 0;
      newNo = m ? Number(m[2]) : 0;
      return { kind: 'hunk', text, n, hunk };
    }
    if (hunk < 0 || text.startsWith('\\')) return { kind: 'meta', text, n, hunk };
    if (text.startsWith('+')) return { kind: 'add', text, n, hunk, newLine: newNo++ };
    if (text.startsWith('-')) return { kind: 'del', text, n, hunk, oldLine: oldNo++ };
    return { kind: 'ctx', text, n, hunk, oldLine: oldNo++, newLine: newNo++ };
  });
}

/** One row of a side-by-side diff. */
export type SplitRow =
  | { kind: 'full'; line: DiffLine }
  | { kind: 'pair'; left?: DiffLine; right?: DiffLine };

/**
 * Pairs a unified diff's lines for a side-by-side view: context lines sit
 * on both sides, a run of deletions is paired line by line with the run of
 * additions that follows it, and headers and hunk markers span the row.
 */
export function splitRows(lines: readonly DiffLine[]): SplitRow[] {
  const rows: SplitRow[] = [];
  let i = 0;
  while (i < lines.length) {
    const l = lines[i];
    if (l.kind === 'meta' || l.kind === 'hunk') {
      rows.push({ kind: 'full', line: l });
      i += 1;
    } else if (l.kind === 'ctx') {
      rows.push({ kind: 'pair', left: l, right: l });
      i += 1;
    } else {
      const dels: DiffLine[] = [];
      const adds: DiffLine[] = [];
      while (i < lines.length && lines[i].kind === 'del') dels.push(lines[i++]);
      while (i < lines.length && lines[i].kind === 'add') adds.push(lines[i++]);
      for (let k = 0; k < Math.max(dels.length, adds.length); k++) {
        rows.push({ kind: 'pair', left: dels[k], right: adds[k] });
      }
    }
  }
  return rows;
}
