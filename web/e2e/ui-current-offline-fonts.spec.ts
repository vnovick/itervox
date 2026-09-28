// CORE-098 — fonts are self-hosted: the dashboard makes no request to Google
// Fonts and Inter / IBM Plex Mono load from the app's own origin, so an
// air-gapped or offline host renders with the intended faces.

import { expect, test, type Page } from '@playwright/test';
import { installMockApi } from './fixtures/mockApi';
import { installMockSse } from './fixtures/mockSse';
import { automationsPassScenario, E2E_TOKEN, quickstartScenario } from './fixtures/scenarios';
import { bootApp } from './fixtures/commands';

/** Every request that leaves the app's own origin is aborted and recorded. */
async function blockExternal(page: Page): Promise<string[]> {
  const external: string[] = [];
  await page.route(
    (url) => url.hostname !== 'localhost' && url.hostname !== '127.0.0.1',
    async (route) => {
      external.push(route.request().url());
      await route.abort('internetdisconnected');
    },
  );
  return external;
}

/**
 * `document.fonts.check()` is vacuously true when no @font-face declares the
 * family (it then means "nothing to load"), so assert a declared face actually
 * reached `loaded`.
 */
async function faceLoaded(page: Page, family: string): Promise<boolean> {
  return page.evaluate(async (fam) => {
    await document.fonts.load(`12px "${fam}"`);
    await document.fonts.ready;
    return [...document.fonts].some(
      (f) => f.family.replace(/["']/g, '') === fam && f.status === 'loaded',
    );
  }, family);
}

test.describe('CORE-098 self-hosted fonts', () => {
  test('Inter and IBM Plex Mono load without any Google Fonts request', async ({ page }) => {
    const fontHosts: string[] = [];
    page.on('request', (req) => {
      if (/fonts\.(googleapis|gstatic)\.com/.test(req.url())) fontHosts.push(req.url());
    });
    await blockExternal(page);
    await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario, 'one-shot');
    await page.goto(`/?token=${encodeURIComponent(E2E_TOKEN)}`);
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    expect(await faceLoaded(page, 'Inter')).toBe(true);
    expect(await faceLoaded(page, 'IBM Plex Mono')).toBe(true);
    expect(await page.evaluate(() => document.fonts.check('12px Inter'))).toBe(true);
    expect(await page.evaluate(() => document.fonts.check('12px "IBM Plex Mono"'))).toBe(true);
    expect(fontHosts).toEqual([]);
  });

  test('with every external host unreachable the dashboard renders without console errors', async ({
    page,
  }) => {
    const errors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') errors.push(msg.text());
    });
    page.on('pageerror', (err) => errors.push(err.message));
    const external = await blockExternal(page);
    await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario, 'one-shot');
    await page.goto(`/?token=${encodeURIComponent(E2E_TOKEN)}`);
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    await expect(page.getByRole('heading', { name: /^Issues 3/ })).toBeVisible();
    expect(external).toEqual([]);
    expect(errors).toEqual([]);
  });
});

test.describe('CORE-098 antd loads with the cron editor only', () => {
  test('the Automations page does not load antd until a cron editor opens', async ({ page }) => {
    const antd: string[] = [];
    page.on('request', (req) => {
      // Dev serves modules by path; the build names the lazy chunk CronWidget-*.js.
      if (/CronWidget|\/antd\/|config-provider|react-js-cron/.test(req.url())) antd.push(req.url());
    });
    await bootApp(page, { scenario: automationsPassScenario, route: '/automations' });
    await expect(page.getByRole('tab', { name: 'Configure' })).toBeVisible();
    await page.waitForLoadState('networkidle');
    expect(antd).toEqual([]);
    await page.getByRole('button', { name: /edit/i }).first().click();
    await expect(page.locator('.react-js-cron')).toBeVisible();
    await expect(page.getByTestId('cron-raw-input')).toBeVisible();
    expect(antd.length).toBeGreaterThan(0);
  });
});
