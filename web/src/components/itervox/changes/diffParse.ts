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
