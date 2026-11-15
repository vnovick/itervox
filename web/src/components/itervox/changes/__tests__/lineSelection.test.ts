import { describe, it, expect } from 'vitest';
import { parseUnifiedDiff } from '../diffParse';
import { describeTarget, inSelection, nextSelection, selectionTarget } from '../lineSelection';

const patch =
  '--- a/a.go\n+++ b/a.go\n@@ -10,3 +10,3 @@\n keep\n-gone\n+added\n@@ -40 +40,2 @@\n ctx\n+tail\n';
const lines = parseUnifiedDiff(patch);

describe('diff line numbers and selection (#238)', () => {
  it('numbers lines from the hunk headers', () => {
    const nums = lines.map((l) => [l.kind, l.oldLine, l.newLine, l.hunk]);
    expect(nums).toEqual([
      ['meta', undefined, undefined, -1],
      ['meta', undefined, undefined, -1],
      ['hunk', undefined, undefined, 0],
      ['ctx', 10, 10, 0],
      ['del', 11, undefined, 0],
      ['add', undefined, 11, 0],
      ['hunk', undefined, undefined, 1],
      ['ctx', 40, 40, 1],
      ['add', undefined, 41, 1],
    ]);
  });

  it('extends a selection only within one hunk', () => {
    const one = nextSelection(lines, null, 3, false);
    expect(nextSelection(lines, one, 5, true)).toEqual({ anchor: 3, focus: 5 });
    expect(nextSelection(lines, one, 8, true)).toEqual({ anchor: 8, focus: 8 });
    expect(inSelection({ anchor: 5, focus: 3 }, 4)).toBe(true);
    expect(inSelection(null, 4)).toBe(false);
  });

  it('turns a selection into a comment target and a quote', () => {
    expect(selectionTarget(lines, { anchor: 4, focus: 4 }, 'a.go')).toEqual({
      target: { path: 'a.go', line: 11, side: 'LEFT' },
      quote: '-gone',
    });
    const range = selectionTarget(lines, { anchor: 5, focus: 3 }, 'a.go');
    expect(range?.target).toEqual({
      path: 'a.go',
      line: 11,
      side: 'RIGHT',
      startLine: 10,
      startSide: 'RIGHT',
    });
    expect(range?.quote).toBe(' keep\n-gone\n+added');
    expect(selectionTarget(lines, { anchor: 2, focus: 2 }, 'a.go')).toBeNull();
  });

  it('describes targets', () => {
    expect(describeTarget({ path: 'a', line: 3, side: 'RIGHT' })).toBe('line 3');
    expect(describeTarget({ path: 'a', line: 3, side: 'LEFT' })).toBe('line 3 (base)');
    expect(
      describeTarget({ path: 'a', line: 5, side: 'RIGHT', startLine: 3, startSide: 'RIGHT' }),
    ).toBe('lines 3–5');
    expect(
      describeTarget({ path: 'a', line: 5, side: 'RIGHT', startLine: 4, startSide: 'LEFT' }),
    ).toBe('lines 4 (base)–5');
  });
});
