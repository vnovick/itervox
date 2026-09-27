#!/usr/bin/env node
// CORE-088 — flags a className string that removes the focus outline
// (`outline-none`, `focus:outline-none`, `focus-visible:outline-none`) without
// adding a replacement focus style in the same literal: `focus:ring-*`,
// `focus:border-*`, `focus:bg-*`, `focus-within:*`, or a `focus-visible:*`
// utility other than `focus-visible:outline-none`. For template literals only
// the literal segments count: a class passed in through `${...}` cannot be
// relied on. Run by `pnpm lint`.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const REMOVES = /(^|\s)((focus|focus-visible):)?outline-none(?=\s|$)/;
const REPLACES = /(^|\s)(focus:(ring|border|bg)-\S+|focus-within:\S+|focus-visible:(?!outline-none(\s|$))\S+)/;
// '...', "..." and `...` literals (escapes honoured; ${...} dropped from templates).
const LITERAL = /'(?:[^'\\\n]|\\.)*'|"(?:[^"\\\n]|\\.)*"|`(?:[^`\\]|\\.)*`/g;

/** @param {string} source @returns {{ line: number, text: string }[]} */
export function findBareOutlines(source) {
  const hits = [];
  for (const match of source.matchAll(LITERAL)) {
    let text = match[0].slice(1, -1);
    if (match[0].startsWith('`')) text = text.replace(/\$\{[^}]*\}/g, ' ');
    if (!REMOVES.test(text) || REPLACES.test(text)) continue;
    const line = source.slice(0, match.index).split('\n').length;
    hits.push({ line, text: text.trim() });
  }
  return hits;
}

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      if (name !== '__tests__' && name !== 'node_modules') yield* walk(path);
    } else if (/\.(tsx|ts)$/.test(name) && !/\.test\.tsx?$/.test(name)) {
      yield path;
    }
  }
}

const isMain = process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href;
if (isMain) {
  const root = process.argv[2] ?? 'src';
  let count = 0;
  for (const file of walk(root)) {
    for (const hit of findBareOutlines(readFileSync(file, 'utf8'))) {
      count += 1;
      console.error(`${relative(process.cwd(), file)}:${hit.line}  bare outline removal: "${hit.text}"`);
    }
  }
  if (count > 0) {
    console.error(
      `\n${count} className(s) remove the focus outline without a replacement focus style ` +
        '(add focus:ring-*, focus:border-*, focus:bg-*, focus-within:* or a focus-visible:* style).',
    );
    process.exit(1);
  }
}
