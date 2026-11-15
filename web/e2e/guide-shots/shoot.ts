// Guide screenshots: one call per guide spec turns a list of shots into one
// test per shot and theme. Each test boots the dashboard against the
// route-mocked API (e2e/fixtures), sets the app's theme before load, waits
// for the shot's state, crops to the shot's region and writes
// site/public/screenshots/guides/<guide>/<slug>-<theme>.webp.
// Run with `make guide-shots` (see README.md in this directory).

import { mkdirSync, realpathSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test, type Locator, type Page } from '@playwright/test';
import { bootApp } from '../fixtures/commands';
import type { MockApiHandle } from '../fixtures/mockApi';
import type { Scenario } from '../fixtures/scenarios';

const here = dirname(fileURLToPath(import.meta.url));
const SITE = resolve(here, '../../../site');
const OUT = process.env.GUIDE_SHOTS_DIR ?? resolve(SITE, 'public/screenshots/guides');
const THEMES = ['dark', 'light'] as const;
export type Theme = (typeof THEMES)[number];

export interface GuideShot {
  /** File name without theme and extension: `<nn>-<slug>`, e.g. `01-held-push`. */
  slug: string;
  scenario: Scenario;
  /** App route, e.g. `/` or `/?issue=DEMO-1`. Default `/`. */
  route?: string;
  /** Extra route mocks, installed after the scenario's and before navigation. */
  mocks?: (api: MockApiHandle) => void | Promise<void>;
  /** Wait for the state the shot shows, open overlays, type text. */
  ready: (page: Page) => Promise<void>;
  /** The region to keep; default the viewport. */
  crop?: (page: Page) => Locator;
  /** CSS px of page kept around `crop` (default 12). */
  pad?: number;
  /** Viewport in CSS px; default 1440×900. */
  viewport?: { width: number; height: number };
}

// sharp comes with the site's Astro install (site/node_modules); the web
// package does not depend on it.
type Sharp = (input: Buffer) => {
  webp: (o: { quality: number }) => { toBuffer: () => Promise<Buffer> };
};
let sharp: Sharp | undefined;
function loadSharp(): Sharp {
  if (sharp) return sharp;
  try {
    // pnpm links astro into node_modules; sharp sits next to its real path.
    const req = createRequire(realpathSync(resolve(SITE, 'node_modules/astro/package.json')));
    sharp = req('sharp') as Sharp;
    return sharp;
  } catch {
    throw new Error(
      'guide-shots: sharp not found; run `cd site && pnpm install --frozen-lockfile`',
    );
  }
}

async function clipFor(page: Page, target: Locator, pad: number) {
  // Centre the region (its top, when it is taller than the viewport), so a
  // region near the bottom of the page is not cut off. A region taller than
  // the viewport is cut at the viewport's bottom edge.
  await target.evaluate((el) => {
    const fits = el.getBoundingClientRect().height < window.innerHeight;
    el.scrollIntoView({ block: fits ? 'center' : 'start', inline: 'nearest' });
    // A region inside a dialog (the glass issue panel) would show the
    // dashboard through it: hide everything but the dialog and its
    // ancestors (the scrim too), so only the page background shows.
    const dialog = el.closest('[role="dialog"]');
    for (let node: Element | null = dialog; node && node !== document.body; ) {
      const parent: Element | null = node.parentElement;
      for (const sib of Array.from(parent?.children ?? [])) {
        if (sib !== node) (sib as HTMLElement).style.visibility = 'hidden';
      }
      node = parent;
    }
  });
  await page.waitForTimeout(100);
  const box = await target.boundingBox();
  if (!box) throw new Error('guide-shots: the crop region is not visible');
  const size = page.viewportSize() ?? { width: 1440, height: 900 };
  const x = Math.max(0, Math.floor(box.x - pad));
  const y = Math.max(0, Math.floor(box.y - pad));
  return {
    x,
    y,
    width: Math.min(size.width, Math.ceil(box.x + box.width + pad)) - x,
    height: Math.min(size.height, Math.ceil(box.y + box.height + pad)) - y,
  };
}

/** One test per shot and theme for `guide` (the guide's file name, e.g. `lanes`). */
export function defineGuideShots(guide: string, shots: GuideShot[]): void {
  for (const theme of THEMES) {
    test.describe(`${guide} (${theme})`, () => {
      for (const shot of shots) {
        test(shot.slug, async ({ page }) => {
          const errors: string[] = [];
          page.on('pageerror', (e) => errors.push(e.message));
          if (shot.viewport) await page.setViewportSize(shot.viewport);
          // The app keeps its theme in localStorage, not prefers-color-scheme.
          await page.addInitScript((t) => window.localStorage.setItem('theme', t), theme);
          await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: theme });
          await bootApp(page, {
            scenario: shot.scenario,
            route: shot.route ?? '/',
            beforeNavigate: shot.mocks,
          });
          await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
          // The dev server mounts the TanStack Query devtools button.
          await page.addStyleTag({ content: '.tsqd-parent-container{display:none!important}' });
          await shot.ready(page);
          await page.evaluate(() => document.fonts.ready);
          await page.waitForTimeout(300);
          const clip = shot.crop ? await clipFor(page, shot.crop(page), shot.pad ?? 12) : undefined;
          const png = await page.screenshot({ clip, animations: 'disabled', caret: 'hide' });
          const webp = await loadSharp()(png).webp({ quality: 82 }).toBuffer();
          mkdirSync(resolve(OUT, guide), { recursive: true });
          writeFileSync(resolve(OUT, guide, `${shot.slug}-${theme}.webp`), webp);
          expect(errors).toEqual([]);
        });
      }
    });
  }
}
