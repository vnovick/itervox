#!/usr/bin/env node
// #211 — bans whole-snapshot store subscriptions outside the store and SSE
// modules. `generatedAt` changes on every SSE frame, so
// `useItervoxStore((s) => s.snapshot)` re-renders its component (and every
// child that is not memoised) on each frame. Components select the fields
// they read instead: one narrow selector per field, or `useShallow` over an
// object of fields. Run by `pnpm lint`; its test also scans web/src.
//
// Flagged, for any selector passed to useItervoxStore (directly or through
// useShallow): a selector returning `s.snapshot` (also `s['snapshot']`,
// `s.snapshot!`, `s.snapshot as X`, a destructured `({ snapshot }) =>
// snapshot`), a selector returning the whole state (`(s) => s`), an object
// literal carrying `s.snapshot` as a value, and `useItervoxStore()` with no
// selector at all.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import ts from 'typescript';

/** Files allowed to subscribe to the whole snapshot (paths relative to web/src). */
export const ALLOWED_FILES = ['store/itervoxStore.ts', 'hooks/useItervoxSSE.ts'];

const HOOK = 'useItervoxStore';

function unwrap(node) {
  let n = node;
  for (;;) {
    if (
      ts.isParenthesizedExpression(n) ||
      ts.isAsExpression(n) ||
      ts.isNonNullExpression(n) ||
      ts.isSatisfiesExpression(n) ||
      ts.isTypeAssertionExpression(n)
    ) {
      n = n.expression;
    } else {
      return n;
    }
  }
}

function returnedExpressions(fn) {
  if (!fn.body) return [];
  if (!ts.isBlock(fn.body)) return [fn.body];
  const out = [];
  const visit = (node) => {
    if (ts.isFunctionLike(node)) return; // nested functions return for themselves
    if (ts.isReturnStatement(node) && node.expression) out.push(node.expression);
    ts.forEachChild(node, visit);
  };
  ts.forEachChild(fn.body, visit);
  return out;
}

function isSnapshotOf(expr, stateName, snapshotAlias) {
  const e = unwrap(expr);
  if (snapshotAlias && ts.isIdentifier(e) && e.text === snapshotAlias) return true;
  if (!stateName) return false;
  if (ts.isPropertyAccessExpression(e)) {
    return (
      e.name.text === 'snapshot' &&
      ts.isIdentifier(unwrap(e.expression)) &&
      unwrap(e.expression).text === stateName
    );
  }
  if (ts.isElementAccessExpression(e)) {
    const arg = e.argumentExpression;
    return (
      ts.isStringLiteralLike(arg) &&
      arg.text === 'snapshot' &&
      ts.isIdentifier(unwrap(e.expression)) &&
      unwrap(e.expression).text === stateName
    );
  }
  return false;
}

/** Reason the selector subscribes to the whole snapshot, or null. */
function selectorProblem(selector) {
  const fn = unwrap(selector);
  if (!ts.isArrowFunction(fn) && !ts.isFunctionExpression(fn)) return null;
  const param = fn.parameters[0];
  if (!param) return null;
  let stateName = null;
  let snapshotAlias = null;
  if (ts.isIdentifier(param.name)) {
    stateName = param.name.text;
  } else if (ts.isObjectBindingPattern(param.name)) {
    for (const el of param.name.elements) {
      const prop = el.propertyName ?? el.name;
      if (ts.isIdentifier(prop) && prop.text === 'snapshot' && ts.isIdentifier(el.name)) {
        snapshotAlias = el.name.text;
      }
    }
  }
  for (const ret of returnedExpressions(fn)) {
    const r = unwrap(ret);
    if (isSnapshotOf(r, stateName, snapshotAlias)) return 'selector returns the whole snapshot';
    if (stateName && ts.isIdentifier(r) && r.text === stateName) {
      return 'selector returns the whole store state';
    }
    if (ts.isObjectLiteralExpression(r)) {
      for (const p of r.properties) {
        const value = ts.isPropertyAssignment(p)
          ? p.initializer
          : ts.isShorthandPropertyAssignment(p)
            ? p.name
            : null;
        if (value && isSnapshotOf(value, stateName, snapshotAlias)) {
          return 'selector object carries the whole snapshot';
        }
        if (ts.isSpreadAssignment(p) && isSnapshotOf(p.expression, stateName, snapshotAlias)) {
          return 'selector spreads the whole snapshot';
        }
      }
    }
  }
  return null;
}

function isHookCallee(expr) {
  const e = unwrap(expr);
  return (
    (ts.isIdentifier(e) && e.text === HOOK) ||
    (ts.isPropertyAccessExpression(e) && e.name.text === HOOK)
  );
}

/**
 * @param {string} source
 * @param {string} [fileName]
 * @returns {{ line: number, reason: string, text: string }[]}
 */
export function findWholeSnapshotSelectors(source, fileName = 'file.tsx') {
  const sf = ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const hits = [];
  const report = (node, reason) => {
    const { line } = sf.getLineAndCharacterOfPosition(node.getStart(sf));
    hits.push({ line: line + 1, reason, text: node.getText(sf).split('\n')[0] });
  };
  const visit = (node) => {
    if (ts.isCallExpression(node) && isHookCallee(node.expression)) {
      const first = node.arguments[0];
      if (!first) {
        report(node, `${HOOK}() without a selector subscribes to the whole store`);
      } else {
        let selector = unwrap(first);
        // useShallow((s) => …) / useShallow(selector)
        if (
          ts.isCallExpression(selector) &&
          ts.isIdentifier(unwrap(selector.expression)) &&
          unwrap(selector.expression).text === 'useShallow' &&
          selector.arguments[0]
        ) {
          selector = selector.arguments[0];
        }
        const problem = selectorProblem(selector);
        if (problem) report(node, problem);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  return hits;
}

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      if (name !== '__tests__' && name !== 'node_modules') yield* walk(path);
    } else if (
      /\.(tsx|ts)$/.test(name) &&
      !/\.(test|spec)\.tsx?$/.test(name) &&
      !name.endsWith('.d.ts')
    ) {
      yield path;
    }
  }
}

/**
 * Scans a source root (web/src) and returns every banned selector outside
 * ALLOWED_FILES.
 * @param {string} root
 */
export function scanSourceTree(root) {
  const out = [];
  for (const file of walk(root)) {
    const rel = relative(root, file).split(sep).join('/');
    if (ALLOWED_FILES.includes(rel)) continue;
    for (const hit of findWholeSnapshotSelectors(readFileSync(file, 'utf8'), file)) {
      out.push({ file: rel, ...hit });
    }
  }
  return out;
}

const isMain = process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href;
if (isMain) {
  const root = process.argv[2] ?? 'src';
  const hits = scanSourceTree(root);
  for (const hit of hits) {
    console.error(`${join(root, hit.file)}:${hit.line}  ${hit.reason}: ${hit.text}`);
  }
  if (hits.length > 0) {
    console.error(
      `\n${hits.length} whole-snapshot subscription(s). Select the fields the component reads ` +
        '(one selector per field, or useShallow over an object of fields) — see #211.',
    );
    process.exit(1);
  }
}
