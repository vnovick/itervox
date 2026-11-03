// #211 — the bundle budget wired into `make verify` and the Web CI job.
import { describe, expect, it } from 'vitest';
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { BUDGET, checkBundleBudget } from '../../scripts/checkBundleBudget.mjs';

const KB = 1024;
const INDEX = `<script type="module" crossorigin src="/assets/index-a.js"></script>
<link rel="modulepreload" crossorigin href="/assets/vendor-b.js">`;

function chunk(name: string, size: number, content = '') {
  return { name, size, content };
}

const healthy = [
  chunk('index-a.js', 200 * KB),
  chunk('vendor-b.js', 100 * KB),
  chunk('Dashboard-c.js', 150 * KB, 'export const board = 1;'),
  chunk('DepsGraph-d.js', 180 * KB, 'react-flow__node @xyflow/react'),
  chunk('Automations-e.js', 60 * KB),
];

describe('checkBundleBudget', () => {
  it('passes a build within every cap', () => {
    const r = checkBundleBudget(healthy, INDEX);
    expect(r.violations).toEqual([]);
    expect(r.initialJs).toBe(300 * KB);
    expect(r.totalJs).toBe(690 * KB);
  });

  it('fails when xyflow is back in the Dashboard chunk', () => {
    const chunks = healthy.map((c) =>
      c.name.startsWith('Dashboard') ? { ...c, content: 'class="react-flow__edge"' } : c,
    );
    expect(checkBundleBudget(chunks, INDEX).violations).toEqual([
      'Dashboard chunk Dashboard-c.js contains forbidden "react-flow__"',
    ]);
  });

  it('fails a CronWidget chunk over 30 KiB and antd / react-js-cron anywhere', () => {
    const chunks = [...healthy, chunk('CronWidget-f.js', 295 * KB, 'react-js-cron ant-cssinjs')];
    const v = checkBundleBudget(chunks, INDEX).violations;
    expect(v).toContain('CronWidget-f.js contains forbidden "react-js-cron"');
    expect(v).toContain('CronWidget-f.js contains forbidden "ant-cssinjs"');
    expect(v).toContain('CronWidget chunk CronWidget-f.js is 295.0 KiB, over 30.0 KiB');
  });

  it('fails on the total, initial and per-chunk caps', () => {
    const chunks = [
      chunk('index-a.js', BUDGET.initialJsBytes),
      chunk('vendor-b.js', 1),
      chunk('Big-x.js', BUDGET.maxChunkBytes + 1),
      chunk('Pad-y.js', BUDGET.totalJsBytes),
    ];
    const v = checkBundleBudget(chunks, INDEX).violations;
    expect(v.some((m) => m.startsWith('total JS'))).toBe(true);
    expect(v.some((m) => m.startsWith('initial JS'))).toBe(true);
    expect(v.some((m) => m.startsWith('Big-x.js is'))).toBe(true);
  });

  it('fails when index.html points at a missing chunk or has no entry', () => {
    expect(checkBundleBudget([chunk('vendor-b.js', 1)], INDEX).violations).toContain(
      'index.html references missing chunk index-a.js',
    );
    expect(checkBundleBudget(healthy, '<html></html>').violations).toContain(
      'index.html has no entry script',
    );
  });

  it('keeps the #211 acceptance caps', () => {
    const dashboard = BUDGET.chunks.find((c) => c.name === 'Dashboard');
    const cron = BUDGET.chunks.find((c) => c.name === 'CronWidget');
    expect(dashboard?.forbid).toEqual(expect.arrayContaining(['react-flow__', '@xyflow']));
    expect(cron?.maxBytes).toBeLessThanOrEqual(30 * KB);
  });
});

function budgetExit(files: Record<string, string>): number {
  const dir: string = mkdtempSync(join(tmpdir(), 'bundle-budget-'));
  try {
    mkdirSync(join(dir, 'assets'));
    for (const [rel, body] of Object.entries(files)) writeFileSync(join(dir, rel), body);
    execFileSync(process.execPath, [join(process.cwd(), 'scripts', 'checkBundleBudget.mjs'), dir], {
      stdio: 'pipe',
    });
    return 0;
  } catch (err) {
    return (err as { status: number }).status;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

describe('bundle budget CLI (make web-bundle-budget / CI)', () => {
  it('exits 0 on a build within budget', () => {
    expect(
      budgetExit({
        'index.html': INDEX,
        'assets/index-a.js': 'x',
        'assets/vendor-b.js': 'y',
        'assets/Dashboard-c.js': 'z',
      }),
    ).toBe(0);
  });

  it('exits 1 when the Dashboard chunk carries xyflow', () => {
    expect(
      budgetExit({
        'index.html': INDEX,
        'assets/index-a.js': 'x',
        'assets/vendor-b.js': 'y',
        'assets/Dashboard-c.js': 'import "@xyflow/react"',
      }),
    ).toBe(1);
  });

  it('exits 1 when there is no build output', () => {
    expect(budgetExit({})).toBe(1);
  });
});
