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

/** Composite translucent token layers, in order, over an opaque base. */
function stack(t: Record<string, string>, base: RGB, ...layers: string[]): RGB {
  return layers.reduce((bg, name) => over(parse(resolveToken(t, name)), bg), base);
}

// Visual refresh — every surface is translucent, so contrast is measured on
// the colour a surface actually shows: its recipe composited over the page
// (--bg) and over the surfaces it sits on. A panel is --glass on the page; a
// card is --card on a panel; chips float on the page or a panel; sheets
// float on the page; --subtle wells sit inside panels; --surface is the
// opaque backing of toasts and pinned banners.
function surfaces(t: Record<string, string>): Record<string, RGB> {
  const page = solid(t, '--bg');
  const glass = stack(t, page, '--glass');
  return {
    page,
    glass,
    card: stack(t, glass, '--card'),
    'chip on page': stack(t, page, '--chip'),
    'chip on glass': stack(t, glass, '--chip'),
    sheet: stack(t, page, '--sheet'),
    'card on sheet': stack(t, page, '--sheet', '--card'),
    'subtle in glass': stack(t, glass, '--subtle'),
    surface: solid(t, '--surface'),
  };
}

/** The page under each glow at its peak (the centre of the radial light). */
function glowPeaks(t: Record<string, string>): Record<string, RGB> {
  const page = solid(t, '--bg');
  return {
    blue: stack(t, page, '--glow-blue'),
    peach: stack(t, page, '--glow-peach'),
    mint: stack(t, page, '--glow-mint'),
  };
}

/** `.pill-<tone>` from index.css → its background and text tokens. */
function pillTokens(tone: string): { bg: string; fg: string } {
  const body = block(`.pill-${tone} {`);
  const bg = /background:\s*var\((--[\w-]+)\)/.exec(body)?.[1];
  const fg = /color:\s*var\((--[\w-]+)\)/.exec(body)?.[1];
  if (!bg || !fg) throw new Error(`.pill-${tone} must use var() tokens`);
  return { bg, fg };
}

describe('tokens.contrast', () => {
  for (const [name, t] of Object.entries(THEMES)) {
    describe(name, () => {
      for (const text of ['--text', '--text-secondary', '--muted']) {
        it(`${text} meets 4.5:1 on every composited surface`, () => {
          const fg = solid(t, text);
          for (const [surface, bg] of Object.entries(surfaces(t))) {
            expect(ratio(fg, bg), `${text} on ${surface}`).toBeGreaterThanOrEqual(4.5);
          }
        });
      }

      it('--text stays 4.5:1 on a panel and a card even at the peak of each glow', () => {
        const fg = solid(t, '--text');
        for (const [glow, under] of Object.entries(glowPeaks(t))) {
          const glass = stack(t, under, '--glass');
          expect(ratio(fg, glass), `glass over ${glow}`).toBeGreaterThanOrEqual(4.5);
          const card = stack(t, glass, '--card');
          expect(ratio(fg, card), `card over ${glow}`).toBeGreaterThanOrEqual(4.5);
        }
      });

      it('legacy surface tokens alias the glass recipes', () => {
        expect(resolveToken(t, '--bg-elevated')).toBe(resolveToken(t, '--glass'));
        expect(resolveToken(t, '--panel')).toBe(resolveToken(t, '--card'));
        expect(resolveToken(t, '--bg-soft')).toBe(resolveToken(t, '--subtle'));
      });

      it('each opaque -solid fallback matches its recipe composited over the page', () => {
        const s = surfaces(t);
        const pairs: [string, RGB][] = [
          ['--glass-solid', s.glass],
          ['--card-solid', s.card],
          ['--chip-solid', s['chip on page']],
          ['--sheet-solid', s.sheet],
        ];
        for (const [token, composite] of pairs) {
          const fallback = solid(t, token);
          for (let i = 0; i < 3; i++) {
            expect(Math.abs(fallback[i] - composite[i]), token).toBeLessThanOrEqual(3);
          }
        }
      });

      for (const signal of ['success', 'warning', 'danger', 'purple']) {
        it(`--${signal}-text meets 4.5:1 on every surface and on --${signal}-soft over each`, () => {
          const text = solid(t, `--${signal}-text`);
          for (const [surface, bg] of Object.entries(surfaces(t))) {
            expect(ratio(text, bg), surface).toBeGreaterThanOrEqual(4.5);
            const soft = over(parse(resolveToken(t, `--${signal}-soft`)), bg);
            expect(ratio(text, soft), `soft on ${surface}`).toBeGreaterThanOrEqual(4.5);
          }
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
        // The toast tint is layered over the opaque --surface (Toast.tsx).
        const underlay = /linear-gradient\(\$\{soft\}, \$\{soft\}\), var\((--[\w-]+)\)/.exec(
          source,
        )?.[1];
        expect(underlay, 'toast tint must sit on an opaque token').toBeDefined();
        const base = solid(t, underlay ?? '');
        for (const signal of ['success', 'danger']) {
          const bg = over(parse(resolveToken(t, `--${signal}-soft`)), base);
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
      it('--accent-text meets 4.5:1 on every surface and on --accent-soft over each', () => {
        const text = solid(t, '--accent-text');
        for (const [surface, bg] of Object.entries(surfaces(t))) {
          expect(ratio(text, bg), surface).toBeGreaterThanOrEqual(4.5);
          const soft = over(parse(resolveToken(t, '--accent-soft')), bg);
          expect(ratio(text, soft), `soft on ${surface}`).toBeGreaterThanOrEqual(4.5);
        }
      });

      it('white on --accent and --danger (primary / destructive buttons) meets 4.5:1', () => {
        for (const bg of ['--accent', '--danger']) {
          expect(ratio([255, 255, 255], solid(t, bg)), bg).toBeGreaterThanOrEqual(4.5);
        }
      });

      it('--accent-strong on --accent-soft (accent pills) meets 4.5:1 on every surface', () => {
        for (const [surface, bg] of Object.entries(surfaces(t))) {
          const soft = over(parse(resolveToken(t, '--accent-soft')), bg);
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

      it('every header status pill tone (PILL_TONE_CLASS) meets 4.5:1 on the glass top bar and on cards', () => {
        const source: string = readFileSync(resolve(SRC, 'layout/AppHeader.tsx'), 'utf8');
        const map = /PILL_TONE_CLASS[^{]*\{([^}]+)\}/.exec(source)?.[1] ?? '';
        const tones = [...map.matchAll(/'pill-([\w-]+)'/g)].map((m) => m[1]);
        expect(tones.sort()).toEqual(['accent', 'danger', 'neutral', 'ok', 'warn']);
        const s = surfaces(t);
        for (const tone of tones) {
          const { bg, fg } = pillTokens(tone);
          for (const surface of ['glass', 'card', 'page'] as const) {
            const fill = over(parse(resolveToken(t, bg)), s[surface]);
            expect(ratio(solid(t, fg), fill), `.pill-${tone} on ${surface}`).toBeGreaterThanOrEqual(
              4.5,
            );
          }
        }
      });

      it('sidebar count pill (.nav-count) meets 4.5:1', () => {
        const body = block('.nav-count {');
        const bg = /background:\s*var\((--[\w-]+)\)/.exec(body)?.[1] ?? '';
        const fg = /color:\s*var\((--[\w-]+)\)/.exec(body)?.[1] ?? '';
        expect(ratio(solid(t, fg), solid(t, bg))).toBeGreaterThanOrEqual(4.5);
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
