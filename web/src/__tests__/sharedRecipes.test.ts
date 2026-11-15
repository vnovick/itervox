// The shared page recipes added for the visual refresh (index.css): each
// class exists, and the code well's text meets AA in both themes.
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const css: string = readFileSync(resolve(process.cwd(), 'src', 'index.css'), 'utf8');

function rule(selector: string): string {
  const start = css.indexOf(`${selector} {`);
  if (start === -1) throw new Error(`rule not found: ${selector}`);
  return css.slice(start, css.indexOf('}', start));
}

function token(block: string, name: string): string {
  const m = new RegExp(`${name}:\\s*([^;]+);`).exec(block);
  if (!m) throw new Error(`${name} missing`);
  return m[1].trim();
}

type RGB = [number, number, number];
function parse(v: string): { rgb: RGB; a: number } {
  const hex = /^#([0-9a-f]{6})$/i.exec(v);
  if (hex) {
    const n = parseInt(hex[1], 16);
    return { rgb: [(n >> 16) & 255, (n >> 8) & 255, n & 255], a: 1 };
  }
  const m = /^rgba\((\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\)$/.exec(v);
  if (!m) throw new Error(`unsupported colour ${v}`);
  return { rgb: [Number(m[1]), Number(m[2]), Number(m[3])], a: Number(m[4]) };
}
function over(fg: { rgb: RGB; a: number }, bg: RGB): RGB {
  return fg.rgb.map((c, i) => Math.round(c * fg.a + bg[i] * (1 - fg.a))) as RGB;
}
function lum([r, g, b]: RGB): number {
  const l = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * l(r) + 0.7152 * l(g) + 0.0722 * l(b);
}
function contrast(a: RGB, b: RGB): number {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

describe('shared page recipes', () => {
  it.each([
    '.field-label',
    '.tag-chip',
    '.choice-card',
    '.switch',
    '.switch:checked',
    '.stat-tile',
    '.stat-tile-label',
    '.stat-tile-value',
    '.data-table',
    '.code-block',
    '.pill-sm',
  ])('%s is defined', (selector) => {
    expect(rule(selector)).toContain(selector);
  });

  it('colours only through tokens', () => {
    expect(rule('.choice-card')).toContain('var(--chip)');
    expect(rule('.stat-tile')).toContain('var(--card)');
    expect(rule('.code-block')).toContain('var(--code-bg)');
    expect(rule('.switch:checked')).toContain('var(--dot-ok)');
  });

  // The code well sits on a panel over the page; check it over the page
  // colour and over white/black extremes of what can scroll beneath it.
  it.each([
    ['light', "  [data-theme='light']", ['#e9edf5', '#ffffff']],
    ['dark', "  :root,\n  [data-theme='dark']", ['#080c17', '#363942']],
  ] as const)('code text is AA on the code well (%s)', (_name, selector, bases) => {
    const block = rule(selector);
    const bg = parse(token(block, '--code-bg'));
    const fg = parse(token(block, '--code-text'));
    for (const base of bases) {
      const well = over(bg, parse(base).rgb);
      expect(contrast(fg.rgb, well)).toBeGreaterThanOrEqual(4.5);
    }
  });
});
