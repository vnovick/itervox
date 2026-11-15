import { describe, expect, it } from 'vitest';
import { parseUnifiedDiff, splitRows } from '../diffParse';

describe('splitRows', () => {
  it('pairs deletions with the additions that follow and keeps context on both sides', () => {
    const lines = parseUnifiedDiff('--- a/x\n+++ b/x\n@@ -1,4 +1,4 @@\n a\n-b\n-c\n+B\n d\n+e\n');
    const rows = splitRows(lines);
    const show = rows.map((r) =>
      r.kind === 'full' ? r.line.text : `${r.left?.text ?? '∅'} | ${r.right?.text ?? '∅'}`,
    );
    expect(show).toEqual([
      '--- a/x',
      '+++ b/x',
      '@@ -1,4 +1,4 @@',
      ' a |  a',
      '-b | +B',
      '-c | ∅',
      ' d |  d',
      '∅ | +e',
    ]);
  });
});
