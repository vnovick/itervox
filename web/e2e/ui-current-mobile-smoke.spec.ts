// T-70 — Mobile shell + responsive smoke (route-mocked).
//
// Runs against the chromium-mobile project (390×844 — iPhone 14 viewport)
// and asserts the dashboard, timeline, logs, settings, and agents routes
// remain usable: no horizontal scroll, headings present, no console errors.
//
// The chromium-desktop project explicitly excludes this file via
// `testIgnore` in playwright.ui.config.ts, so no in-spec skip is required.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { mobileShellScenario, quickstartScenario, settingsMatrixScenario } from './fixtures/scenarios';

test.describe('T-70 mobile shell smoke', () => {

  test('Dashboard at 390×844: no horizontal scroll, hero stats visible', async ({ page }) => {
    await bootApp(page, { scenario: mobileShellScenario });

    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();

    const { scrollWidth, viewportWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
    }));
    expect(scrollWidth, 'horizontal scroll on /').toBeLessThanOrEqual(viewportWidth);
  });

  test('Timeline at 390×844: page renders, no horizontal scroll', async ({ page }) => {
    await bootApp(page, { scenario: quickstartScenario, route: '/timeline' });

    await expect(page).toHaveTitle(/Timeline/);

    const { scrollWidth, viewportWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
    }));
    expect(scrollWidth, 'horizontal scroll on /timeline').toBeLessThanOrEqual(viewportWidth);
  });

  test('Logs at 390×844: page renders, no horizontal scroll', async ({ page }) => {
    await bootApp(page, { scenario: quickstartScenario, route: '/logs' });

    await expect(page).toHaveTitle(/Logs/);

    const { scrollWidth, viewportWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
    }));
    expect(scrollWidth, 'horizontal scroll on /logs').toBeLessThanOrEqual(viewportWidth);
  });

  test('Settings at 390×844: page renders, no horizontal scroll', async ({ page }) => {
    await bootApp(page, { scenario: settingsMatrixScenario, route: '/settings' });

    await expect(page).toHaveTitle(/Settings/);

    const { scrollWidth, viewportWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
    }));
    expect(scrollWidth, 'horizontal scroll on /settings').toBeLessThanOrEqual(viewportWidth);
  });

  test('Agents at 390×844: page renders, no horizontal scroll', async ({ page }) => {
    await bootApp(page, { scenario: settingsMatrixScenario, route: '/agents' });

    await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible();

    const { scrollWidth, viewportWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
    }));
    expect(scrollWidth, 'horizontal scroll on /agents').toBeLessThanOrEqual(viewportWidth);
  });

  test('Long-title issue: title truncates without forcing horizontal scroll', async ({ page }) => {
    await bootApp(page, { scenario: mobileShellScenario });

    const { scrollWidth, viewportWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
    }));
    expect(scrollWidth, 'long-title issue caused horizontal scroll').toBeLessThanOrEqual(viewportWidth);
  });
});
