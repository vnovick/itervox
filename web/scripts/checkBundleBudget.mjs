#!/usr/bin/env node
// #211 — bundle budget for the embedded dashboard. Reads the `vite build`
// output (internal/server/web/dist by default) and fails when a chunk, the
// initial load or the total JavaScript grows past its cap, or when a chunk
// regains a dependency it must not carry (xyflow in the Dashboard chunk,
// antd / react-js-cron anywhere). Run after `pnpm build` by `pnpm
// bundle:budget`, `make web-bundle-budget` (part of `make verify`) and the
// Web CI job. Raise a cap deliberately, in the same change that needs it.

import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

const KB = 1024;

export const BUDGET = {
  /**
   * All JavaScript in assets/ (before #211: 2,118,115 bytes). Raised from
   * 1900 KiB by #251: the New task form (+8.4 KiB in the Dashboard chunk)
   * and its lazy issue-detail and query chunks took the total to ~1901 KiB.
   */
  totalJsBytes: 1920 * KB,
  /** The entry script plus its modulepreloads from index.html (before #211: 499,682). */
  initialJsBytes: 520 * KB,
  /** Any single chunk (the CodeMirror prompt editor is the largest, ~393 kB). */
  maxChunkBytes: 410 * KB,
  /** Per-chunk caps and forbidden markers, matched on the hashed file name. */
  chunks: [
    {
      // Before #211: 361,077 bytes, with @xyflow/react bundled in.
      name: 'Dashboard',
      pattern: /^Dashboard-[\w-]+\.js$/,
      maxBytes: 200 * KB,
      forbid: ['react-flow__', '@xyflow'],
    },
    {
      // Before #211: 295,055 bytes of antd + react-js-cron. The light cron
      // input lives in the Automations chunk now; a CronWidget chunk, if one
      // ever comes back, must stay small.
      name: 'CronWidget',
      pattern: /^CronWidget-[\w-]+\.js$/,
      maxBytes: 30 * KB,
      forbid: [],
    },
  ],
  /** Markers no chunk may contain: antd (css-in-js runtime) and react-js-cron. */
  forbidEverywhere: ['react-js-cron', 'ant-cssinjs'],
};

/**
 * @param {{ name: string, size: number, content: string }[]} chunks  JS files in assets/
 * @param {string} indexHtml
 * @param {typeof BUDGET} budget
 * @returns {{ violations: string[], totalJs: number, initialJs: number }}
 */
export function checkBundleBudget(chunks, indexHtml, budget = BUDGET) {
  const violations = [];
  const byName = new Map(chunks.map((c) => [c.name, c]));
  const totalJs = chunks.reduce((sum, c) => sum + c.size, 0);
  const initialNames = [...indexHtml.matchAll(/(?:src|href)="\/?assets\/([^"]+\.js)"/g)].map(
    (m) => m[1],
  );
  let initialJs = 0;
  for (const name of new Set(initialNames)) {
    const chunk = byName.get(name);
    if (!chunk) violations.push(`index.html references missing chunk ${name}`);
    else initialJs += chunk.size;
  }
  if (initialNames.length === 0) violations.push('index.html has no entry script');

  if (totalJs > budget.totalJsBytes) {
    violations.push(`total JS ${fmt(totalJs)} exceeds ${fmt(budget.totalJsBytes)}`);
  }
  if (initialJs > budget.initialJsBytes) {
    violations.push(`initial JS ${fmt(initialJs)} exceeds ${fmt(budget.initialJsBytes)}`);
  }
  for (const c of chunks) {
    if (c.size > budget.maxChunkBytes) {
      violations.push(
        `${c.name} is ${fmt(c.size)}, over the per-chunk cap ${fmt(budget.maxChunkBytes)}`,
      );
    }
    for (const marker of budget.forbidEverywhere) {
      if (c.content.includes(marker)) violations.push(`${c.name} contains forbidden "${marker}"`);
    }
    for (const rule of budget.chunks) {
      if (!rule.pattern.test(c.name)) continue;
      if (c.size > rule.maxBytes) {
        violations.push(
          `${rule.name} chunk ${c.name} is ${fmt(c.size)}, over ${fmt(rule.maxBytes)}`,
        );
      }
      for (const marker of rule.forbid) {
        if (c.content.includes(marker)) {
          violations.push(`${rule.name} chunk ${c.name} contains forbidden "${marker}"`);
        }
      }
    }
  }
  return { violations, totalJs, initialJs };
}

function fmt(bytes) {
  return `${(bytes / KB).toFixed(1)} KiB`;
}

/** @param {string} distDir */
export function readDist(distDir) {
  const assets = join(distDir, 'assets');
  const indexPath = join(distDir, 'index.html');
  if (!existsSync(indexPath) || !existsSync(assets)) {
    throw new Error(`no build output in ${distDir} — run \`pnpm build\` first`);
  }
  const chunks = readdirSync(assets)
    .filter((name) => name.endsWith('.js'))
    .map((name) => {
      const path = join(assets, name);
      return { name, size: statSync(path).size, content: readFileSync(path, 'utf8') };
    });
  return { chunks, indexHtml: readFileSync(indexPath, 'utf8') };
}

const isMain = process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href;
if (isMain) {
  const distDir = process.argv[2] ?? join('..', 'internal', 'server', 'web', 'dist');
  let result;
  try {
    const { chunks, indexHtml } = readDist(distDir);
    result = checkBundleBudget(chunks, indexHtml);
    const largest = [...chunks].sort((a, b) => b.size - a.size).slice(0, 5);
    console.log(
      `bundle-budget: total JS ${fmt(result.totalJs)} / ${fmt(BUDGET.totalJsBytes)}, ` +
        `initial JS ${fmt(result.initialJs)} / ${fmt(BUDGET.initialJsBytes)}`,
    );
    for (const c of largest) console.log(`  ${c.name.padEnd(40)} ${fmt(c.size)}`);
  } catch (err) {
    console.error(`bundle-budget: ${err.message}`);
    process.exit(1);
  }
  if (result.violations.length > 0) {
    for (const v of result.violations) console.error(`bundle-budget: ${v}`);
    process.exit(1);
  }
  console.log('bundle-budget: within budget');
}
