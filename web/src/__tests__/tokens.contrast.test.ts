// CORE-071 — WCAG 2.x contrast for the design tokens in src/index.css.
// Reads the stylesheet itself so a token edit that regresses contrast fails
// here, not in a manual audit. AA: 4.5:1 for text, 3:1 for UI components.
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

// node:fs / node:path are untyped under tsconfig.test.json, hence the explicit
// `: string` annotations. Vitest runs from web/ (same convention as schemas.parity.test.ts).
const SRC: string = resolve(process.cwd(), 'src');
const css: string = readFileSync(resolve(SRC, 'index.css'), 'utf8');

function block(selector: string): string {
  const start = css.indexOf(selector);
  if (start === -1) throw new Error(`selector not found: ${selector}`);
  const open = css.indexOf('{', start);
  const close = css.indexOf('}', open);
  return css.slice(open + 1, close);
}

function tokens(body: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/(--[\w-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}

const dark = tokens(block(":root,\n[data-theme='dark'] {"));
const light = { ...dark, ...tokens(block("[data-theme='light'] {")) };
const theme = tokens(block('@theme {'));
const THEMES = { dark, light } as const;

type RGB = [number, number, number];

function parse(value: string): { rgb: RGB; alpha: number } {
  const hex = /^#([0-9a-f]{6})$/i.exec(value);
  if (hex) {
    const n = parseInt(hex[1], 16);
    return { rgb: [(n >> 16) & 255, (n >> 8) & 255, n & 255], alpha: 1 };
  }
  const rgba = /^rgba\((\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\)$/.exec(value);
  if (rgba) {
    return {
      rgb: [Number(rgba[1]), Number(rgba[2]), Number(rgba[3])],
      alpha: Number(rgba[4]),
    };
  }
  throw new Error(`unsupported colour: ${value}`);
}

function resolveToken(t: Record<string, string>, name: string): string {
  const v = t[name] as string | undefined;
  if (v === undefined) throw new Error(`token ${name} is not defined`);
  const ref = /^var\((--[\w-]+)\)$/.exec(v);
  return ref ? resolveToken(t, ref[1]) : v;
}

function over(fg: { rgb: RGB; alpha: number }, bg: RGB): RGB {
  return fg.rgb.map((c, i) => Math.round(c * fg.alpha + bg[i] * (1 - fg.alpha))) as RGB;
}

function luminance([r, g, b]: RGB): number {
  const lin = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

function ratio(a: RGB, b: RGB): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

function solid(t: Record<string, string>, name: string): RGB {
  const c = parse(resolveToken(t, name));
  if (c.alpha !== 1) throw new Error(`${name} is not opaque`);
  return c.rgb;
}

describe('tokens.contrast', () => {
  for (const [name, t] of Object.entries(THEMES)) {
    describe(name, () => {
      it('--muted meets 4.5:1 on --panel, --bg and --bg-soft', () => {
        for (const surface of ['--panel', '--bg', '--bg-soft']) {
          expect(ratio(solid(t, '--muted'), solid(t, surface)), surface).toBeGreaterThanOrEqual(
            4.5,
          );
        }
      });

      for (const signal of ['success', 'warning', 'danger']) {
        it(`--${signal}-text meets 4.5:1 on --panel and on --${signal}-soft composited over --panel`, () => {
          const text = solid(t, `--${signal}-text`);
          const panel = solid(t, '--panel');
          expect(ratio(text, panel)).toBeGreaterThanOrEqual(4.5);
          const soft = over(parse(resolveToken(t, `--${signal}-soft`)), panel);
          expect(ratio(text, soft)).toBeGreaterThanOrEqual(4.5);
        });
      }

      it('Terminal timestamps (--muted on --bg, at the opacity Terminal renders them) meet 4.5:1', () => {
        const source: string = readFileSync(
          resolve(SRC, 'components/ui/Terminal/Terminal.tsx'),
          'utf8',
        );
        const span = /data-testid=\{`terminal-time-[^>]*>/s.exec(source)?.[0] ?? '';
        const opacity = Number(/opacity-(\d+)/.exec(span)?.[1] ?? '100') / 100;
        const fg = over({ rgb: solid(t, '--muted'), alpha: opacity }, solid(t, '--bg'));
        expect(ratio(fg, solid(t, '--bg'))).toBeGreaterThanOrEqual(4.5);
      });

      it('toast dismiss control (signal text at its opacity over the composited toast) meets 3:1', () => {
        const source: string = readFileSync(resolve(SRC, 'components/common/Toast.tsx'), 'utf8');
        const button = /aria-label="Dismiss notification"[^>]*>/s.exec(source)?.[0] ?? '';
        const opacity = Number(/opacity-(\d+)/.exec(button)?.[1] ?? '100') / 100;
        const panel = solid(t, '--panel');
        for (const signal of ['success', 'danger']) {
          const bg = over(parse(resolveToken(t, `--${signal}-soft`)), panel);
          const fg = over({ rgb: solid(t, `--${signal}-text`), alpha: opacity }, bg);
          expect(ratio(fg, bg), signal).toBeGreaterThanOrEqual(3);
        }
      });
    });
  }

  // M5-close — the accent family and two M5 surfaces the verifier measured
  // below AA: the attention-inbox Send button (3.01:1, white on orange-500)
  // and the header Resuming pill (3.52:1 in light).
  for (const [name, t] of Object.entries(THEMES)) {
    describe(`${name} accent`, () => {
      it('--accent-text meets 4.5:1 on --panel, --bg, --bg-soft and --accent-soft over --panel', () => {
        const text = solid(t, '--accent-text');
        for (const surface of ['--panel', '--bg', '--bg-soft']) {
          expect(ratio(text, solid(t, surface)), surface).toBeGreaterThanOrEqual(4.5);
        }
        const soft = over(parse(resolveToken(t, '--accent-soft')), solid(t, '--panel'));
        expect(ratio(text, soft)).toBeGreaterThanOrEqual(4.5);
      });

      it('white on --accent and --danger (primary / destructive buttons) meets 4.5:1', () => {
        for (const bg of ['--accent', '--danger']) {
          expect(ratio([255, 255, 255], solid(t, bg)), bg).toBeGreaterThanOrEqual(4.5);
        }
      });

      it('--accent-strong on --accent-soft (accent pills) meets 4.5:1 over --panel and --bg-soft', () => {
        for (const surface of ['--panel', '--bg-soft']) {
          const soft = over(parse(resolveToken(t, '--accent-soft')), solid(t, surface));
          expect(ratio(solid(t, '--accent-strong'), soft), surface).toBeGreaterThanOrEqual(4.5);
        }
      });

      it('inbox Send button (InputReplyBox) meets 4.5:1', () => {
        const source: string = readFileSync(
          resolve(SRC, 'components/itervox/InputReplyBox.tsx'),
          'utf8',
        );
        const button =
          /data-testid="input-reply-send"[\s\S]*?className="([^"]+)"/.exec(source)?.[1] ?? '';
        const bg = /\bbg-theme-([\w-]+)/.exec(button)?.[1];
        expect(bg, 'Send button must use a theme background token').toBeDefined();
        expect(button).toMatch(/\btext-white\b/);
        expect(ratio([255, 255, 255], solid(t, `--${bg ?? ''}`))).toBeGreaterThanOrEqual(4.5);
      });

      it('header Resuming pill (PILL_TONE_CLASS.info on the header --bg-soft) meets 4.5:1', () => {
        const source: string = readFileSync(resolve(SRC, 'layout/AppHeader.tsx'), 'utf8');
        const info = /info:\s*'([^']+)'/.exec(source)?.[1] ?? '';
        const bgToken = /\bbg-theme-([\w-]+)/.exec(info)?.[1] ?? '';
        const fgToken = /\btext-theme-([\w-]+)/.exec(info)?.[1] ?? '';
        const bg = over(parse(resolveToken(t, `--${bgToken}`)), solid(t, '--bg-soft'));
        expect(ratio(solid(t, `--${fgToken}`), bg)).toBeGreaterThanOrEqual(4.5);
      });
    });
  }

  it('Logs placeholder and label tokens meet 4.5:1 on the terminal surfaces', () => {
    const surfaces = [
      '--color-terminal-base',
      '--color-terminal-header',
      '--color-terminal-void',
      '--color-terminal-input',
    ];
    for (const fg of ['--color-terminal-dim', '--color-terminal-label']) {
      for (const bg of surfaces) {
        expect(ratio(solid(theme, fg), solid(theme, bg)), `${fg} on ${bg}`).toBeGreaterThanOrEqual(
          4.5,
        );
      }
    }
  });

  it('Logs page uses the tokens, not the failing hex literals', () => {
    const logs: string = readFileSync(resolve(SRC, 'pages/Logs/index.tsx'), 'utf8');
    expect(logs).not.toMatch(/#374151/);
    expect(logs).not.toMatch(/#4b5563/);
  });
});
