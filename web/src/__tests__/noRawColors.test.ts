// Visual refresh — colours live in the design tokens (src/index.css) and the
// shared primitives, never as literals in component or page code. This scans
// components/, pages/, layout/ and App.tsx for raw hex / rgb() / hsl()
// colours, Tailwind arbitrary hex classes (`bg-[#…]`) and Tailwind palette
// colour classes (`text-emerald-300`, `bg-zinc-800`), so a regression fails
// CI. Use a theme token (`text-theme-success-text`, `var(--warning-soft)`)
// or a primitive / glass class instead.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const SRC: string = resolve(process.cwd(), 'src');
const ROOTS = ['components', 'pages', 'layout', 'App.tsx'];

/** Files allowed to keep a literal, with the reason. Keep this list short. */
const ALLOW: Record<string, string> = {
  // A QR code must stay black on white whatever the theme, or phones cannot scan it.
  'components/itervox/hubPhone/QrCode.tsx': 'QR modules need fixed black-on-white',
};

const HEX = /(?<![\w&])#(?![0-9]+\b)[0-9a-fA-F]{3,8}\b/g;
const FUNC = /\b(?:rgba?|hsla?)\(/g;
const ARBITRARY = /-\[#[0-9a-fA-F]+\]/g;
const PALETTE =
  /\b(?:bg|text|border|ring|from|to|via|fill|stroke|outline|divide|shadow)-(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d{2,3}\b/g;

// node:fs / node:path are untyped under tsconfig.test.json, hence the
// explicit annotations (same convention as tokens.contrast.test.ts).
function* walk(path: string): Generator<string> {
  const isDir: boolean = statSync(path).isDirectory();
  if (isDir) {
    const names: string[] = readdirSync(path);
    for (const name of names) {
      if (name === '__tests__' || name === 'node_modules') continue;
      const child: string = join(path, name);
      yield* walk(child);
    }
  } else if (/\.(tsx?|mts)$/.test(path) && !/\.(test|spec)\.tsx?$/.test(path)) {
    yield path;
  }
}

/** Drop comments so prose such as "#218" or "rgba(…) used to" never counts. */
function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`])\/\/.*$/gm, '$1');
}

function findRawColors(source: string): string[] {
  const code = stripComments(source);
  return [HEX, FUNC, ARBITRARY, PALETTE].flatMap((re) => [...code.matchAll(re)].map((m) => m[0]));
}

describe('no raw colours outside the token file', () => {
  it('detects each kind of literal (self-check)', () => {
    expect(findRawColors(`const a = '#ef4444'; // #218 is an issue`)).toEqual(['#ef4444']);
    expect(findRawColors(`style={{ background: 'rgba(0,0,0,0.25)' }}`)).toEqual(['rgba(']);
    expect(findRawColors(`className="bg-[#111] text-emerald-300"`)).toEqual([
      '-[#111]',
      'text-emerald-300',
    ]);
    expect(findRawColors(`className="text-theme-success-text bg-theme-warning-soft"`)).toEqual([]);
  });

  it('components, pages and layout use tokens and primitives only', () => {
    const offenders: string[] = [];
    for (const root of ROOTS) {
      const start: string = join(SRC, root);
      for (const file of walk(start)) {
        const relPath: string = relative(SRC, file);
        const rel = relPath.split('\\').join('/');
        if (rel in ALLOW) continue;
        const text: string = readFileSync(file, 'utf8');
        const hits = findRawColors(text);
        if (hits.length > 0) offenders.push(`${rel}: ${[...new Set(hits)].join(', ')}`);
      }
    }
    expect(offenders).toEqual([]);
  });
});
