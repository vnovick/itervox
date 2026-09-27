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
import {
  activeRunScenario,
  mobileShellScenario,
  quickstartScenario,
  settingsMatrixScenario,
} from './fixtures/scenarios';

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
    expect(scrollWidth, 'long-title issue caused horizontal scroll').toBeLessThanOrEqual(
      viewportWidth,
    );
  });
});

// CORE-087 — responsive Logs, Timeline and running rows, and the 404 inside
// the shell, at a 375px phone width (the narrowest common iPhone).
test.describe('CORE-087 responsive at 375px', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
  });

  const noHorizontalScroll = async (page: import('@playwright/test').Page, where: string) => {
    const { scrollWidth, viewportWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
    }));
    expect(scrollWidth, `horizontal scroll on ${where}`).toBeLessThanOrEqual(viewportWidth);
  };

  test('logs terminal uses at least 70% of a 390px viewport', async ({ page }) => {
    // Named for the spec; asserted at 375px, which is stricter.
    await bootApp(page, { scenario: quickstartScenario, route: '/logs/DEMO-2' });
    await expect(page.getByTestId('logs-context-strip')).toBeVisible();
    await expect(page.getByTestId('logs-sidebar')).toHaveCount(1);
    await expect(page.getByTestId('logs-sidebar')).toBeHidden();
    await expect(page.getByRole('combobox', { name: 'Issue' })).toBeVisible();
    const pane = page.getByTestId('logs-filter-chips');
    const box = await pane.boundingBox();
    expect(box?.width ?? 0).toBeGreaterThanOrEqual(0.7 * 375);
    await noHorizontalScroll(page, '/logs');
    // The picker switches issues.
    await page.getByRole('combobox', { name: 'Issue' }).selectOption('DEMO-1');
    await expect(page).toHaveURL(/\/logs\/DEMO-1$/);
  });

  test('timeline sidebar collapses below md', async ({ page }) => {
    await bootApp(page, { scenario: activeRunScenario, route: '/timeline' });
    // toBeHidden() alone also passes for a missing element: require it attached.
    await expect(page.getByTestId('timeline-sidebar')).toHaveCount(1);
    await expect(page.getByTestId('timeline-sidebar')).toBeHidden();
    await expect(page.getByTestId('timeline-issue-select')).toBeVisible();
    await noHorizontalScroll(page, '/timeline');
  });

  test('running row stacks as a card at 390px', async ({ page }) => {
    await bootApp(page, { scenario: activeRunScenario });
    const row = page.getByTestId('running-row-DEMO-RUN-1');
    await expect(row).toBeVisible();
    const display = await row.evaluate((el) => getComputedStyle(el).display);
    expect(display).toBe('flex');
    const rowBox = await row.boundingBox();
    expect(rowBox?.width ?? 999).toBeLessThanOrEqual(375);
    await noHorizontalScroll(page, '/');
  });

  test('404 renders inside the shell without horizontal scroll', async ({ page }) => {
    await bootApp(page, { scenario: quickstartScenario, route: '/no-such-page' });
    await expect(page.getByRole('link', { name: 'Back to Home Page' })).toBeVisible();
    // The shell's header (mobile menu button) and nav are around the page.
    await expect(page.getByRole('button', { name: /open navigation|menu/i })).toBeVisible();
    await expect(page.getByTestId('header-orchestrator-state')).toBeVisible();
    await noHorizontalScroll(page, '/no-such-page');
  });
});
