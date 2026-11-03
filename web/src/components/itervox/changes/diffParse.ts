/** One rendered line of a unified diff. */
export interface DiffLine {
  kind: 'meta' | 'hunk' | 'add' | 'del' | 'ctx';
  text: string;
  /** Position in the patch; with the text it keys the row. */
  n: number;
}

/** Classifies each line of a unified diff (git's patch format). */
export function parseUnifiedDiff(patch: string): DiffLine[] {
  const lines = patch.endsWith('\n') ? patch.slice(0, -1).split('\n') : patch.split('\n');
  let inHunk = false;
  return lines.map((text, n) => {
    if (text.startsWith('@@')) {
      inHunk = true;
      return { kind: 'hunk', text, n };
    }
    if (!inHunk) return { kind: 'meta', text, n };
    if (text.startsWith('+')) return { kind: 'add', text, n };
    if (text.startsWith('-')) return { kind: 'del', text, n };
    if (text.startsWith('\\')) return { kind: 'meta', text, n };
    return { kind: 'ctx', text, n };
  });
}
