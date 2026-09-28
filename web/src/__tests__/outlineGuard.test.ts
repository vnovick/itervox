// CORE-088 — the bare outline-none guard wired into `pnpm lint`.
import { describe, expect, it } from 'vitest';
import { findBareOutlines } from '../../scripts/outlineGuard.mjs';
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const tsx = (cls: string) => `export const A = () => <input className="${cls}" />;\n`;

describe('outline guard', () => {
  it.each([
    'focus:ring-1 focus:outline-none',
    'outline-none focus:border-x',
    'focus:bg-x focus:outline-none',
    'focus-visible:outline-none focus-visible:ring-2',
    'outline-none focus-within:ring-1',
    'rounded px-2 text-sm',
  ])('passes %s', (cls) => {
    expect(findBareOutlines(tsx(cls))).toEqual([]);
  });

  it.each(['focus:outline-none', 'focus-visible:outline-none', 'rounded outline-none'])(
    'fails %s',
    (cls) => {
      const hits = findBareOutlines(tsx(cls));
      expect(hits).toHaveLength(1);
      expect(hits[0].line).toBe(1);
    },
  );

  it('evaluates only the literal segments of a template className', () => {
    // A caller could pass a ring in ${inputClassName}, but it cannot be relied on.
    const src = 'const c = `h-8 focus:outline-none ${inputClassName}`;\n';
    expect(findBareOutlines(src)).toHaveLength(1);
    const ok = 'const c = `h-8 focus:outline-none focus:ring-1 ${x}`;\n';
    expect(findBareOutlines(ok)).toEqual([]);
  });

  it('reports the line of each offending literal', () => {
    const src = "const a = 'ok';\nconst b = 'focus:outline-none';\nconst c = \"outline-none\";\n";
    expect(findBareOutlines(src).map((h) => h.line)).toEqual([2, 3]);
  });
});

// CORE-088 acceptance matrix, run through the CLI that `pnpm lint` calls: one
// fixture file per class list, exit status 0 (pass) or 1 (fail).

function guardExit(cls: string): number {
  const dir: string = mkdtempSync(join(tmpdir(), 'outline-guard-'));
  try {
    mkdirSync(join(dir, 'src'));
    writeFileSync(
      join(dir, 'src', 'Fixture.tsx'),
      `export const F = () => <input className="${cls}" />;\n`,
    );
    execFileSync(
      process.execPath,
      [join(process.cwd(), 'scripts', 'outlineGuard.mjs'), join(dir, 'src')],
      {
        stdio: 'pipe',
      },
    );
    return 0;
  } catch (err) {
    return (err as { status: number }).status;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

describe('outline guard CLI (the lint step)', () => {
  it.each([
    'focus:ring-1 focus:outline-none',
    'outline-none focus:border-x',
    'focus:bg-x focus:outline-none',
  ])('exits 0 on a fixture containing %s', (cls) => {
    expect(guardExit(cls)).toBe(0);
  });

  it.each(['focus:outline-none', 'focus-visible:outline-none'])(
    'exits 1 on a fixture containing only %s',
    (cls) => {
      expect(guardExit(cls)).toBe(1);
    },
  );
});
