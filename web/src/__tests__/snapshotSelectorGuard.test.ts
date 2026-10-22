// #211 — the no-whole-snapshot-selector guard. `generatedAt` changes on every
// SSE frame, so a component subscribed to the whole snapshot re-renders on
// each one. Only the store and the SSE hook may subscribe to it.
import { describe, expect, it } from 'vitest';
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  ALLOWED_FILES,
  findWholeSnapshotSelectors,
  scanSourceTree,
} from '../../scripts/snapshotSelectorGuard.mjs';

describe('no-whole-snapshot-selector guard', () => {
  it('finds no whole-snapshot subscription in web/src outside the store and SSE modules', () => {
    // Vitest runs from web/, like `pnpm lint`.
    expect(scanSourceTree('src')).toEqual([]);
  });

  it('allows only the store and SSE modules', () => {
    expect(ALLOWED_FILES).toEqual(['store/itervoxStore.ts', 'hooks/useItervoxSSE.ts']);
  });

  it.each([
    'const s = useItervoxStore((s) => s.snapshot);',
    'const s = useItervoxStore((state) => state.snapshot);',
    'const s = useItervoxStore((st: ItervoxStore) => st.snapshot);',
    'const s = useItervoxStore((s) => s.snapshot!);',
    "const s = useItervoxStore((s) => s['snapshot']);",
    'const s = useItervoxStore((s) => (s.snapshot as StateSnapshot));',
    'const s = useItervoxStore(({ snapshot }) => snapshot);',
    'const s = useItervoxStore(({ snapshot: snap }) => snap);',
    'const s = useItervoxStore(function (s) { return s.snapshot; });',
    'const s = useItervoxStore((s) => { const x = 1; return s.snapshot; });',
    'const s = useItervoxStore(useShallow((s) => ({ snap: s.snapshot, n: 1 })));',
    'const s = useItervoxStore(useShallow((s) => ({ ...s.snapshot })));',
    'const s = useItervoxStore((s) => s);',
    'const s = useItervoxStore();',
  ])('flags %s', (src) => {
    expect(findWholeSnapshotSelectors(src)).toHaveLength(1);
  });

  it.each([
    'const r = useItervoxStore((s) => s.snapshot?.running ?? EMPTY);',
    'const r = useItervoxStore((s) => s.snapshot !== null);',
    'const r = useItervoxStore(useShallow((s) => ({ running: s.snapshot?.running })));',
    'const r = useItervoxStore((s) => s.setSnapshot);',
    'const r = useItervoxStore.getState().snapshot;',
    'const unsub = useItervoxStore.subscribe((s) => { void s.snapshot; });',
    'const r = useOtherStore((s) => s.snapshot);',
    'const r = useItervoxStore((s) => s.snapshot?.running.map((x) => x.snapshot));',
  ])('allows %s', (src) => {
    expect(findWholeSnapshotSelectors(src)).toEqual([]);
  });

  it('reports the line of each offending call', () => {
    const src = 'const a = 1;\nconst b = useItervoxStore((s) => s.snapshot);\n';
    expect(findWholeSnapshotSelectors(src).map((h) => h.line)).toEqual([2]);
  });
});

function guardExit(files: Record<string, string>): number {
  const dir: string = mkdtempSync(join(tmpdir(), 'snapshot-guard-'));
  try {
    for (const [rel, body] of Object.entries(files)) {
      const path = join(dir, 'src', rel);
      mkdirSync(join(path, '..'), { recursive: true });
      writeFileSync(path, body);
    }
    execFileSync(
      process.execPath,
      [join(process.cwd(), 'scripts', 'snapshotSelectorGuard.mjs'), join(dir, 'src')],
      { stdio: 'pipe' },
    );
    return 0;
  } catch (err) {
    return (err as { status: number }).status;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

describe('no-whole-snapshot-selector guard CLI (the lint step)', () => {
  const bad = 'export const x = () => useItervoxStore((s) => s.snapshot);\n';
  it('exits 1 on a component subscribing to the whole snapshot', () => {
    expect(guardExit({ 'components/Foo.tsx': bad })).toBe(1);
  });
  it('exits 0 when the subscription is in the store or SSE module', () => {
    expect(guardExit({ 'store/itervoxStore.ts': bad, 'hooks/useItervoxSSE.ts': bad })).toBe(0);
  });
  it('ignores test files', () => {
    expect(guardExit({ 'components/__tests__/Foo.test.tsx': bad, 'x/Foo.test.ts': bad })).toBe(0);
  });
});
