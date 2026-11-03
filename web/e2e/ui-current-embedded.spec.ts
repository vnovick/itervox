// Embedded mode (the desktop app's dashboard window) and the issue view
// (route-mocked). The app's webview initialization script sets
// window.__ITERVOX_SHELL__ = 'desktop' before the bundle runs; these specs
// do the same with addInitScript. `?embedded=1` is the dev-server switch.

import { expect, test, type Page } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { installDiffData, installRunContextData } from './fixtures/tourData';
import type { MockApiHandle } from './fixtures/mockApi';
import {
  activeRunScenario,
  emptyScenario,
  inputRequiredScenario,
  type Scenario,
} from './fixtures/scenarios';

const withRunContext = (scenario: Scenario): Scenario => ({
  ...scenario,
  snapshot: {
    ...scenario.snapshot,
    projectName: 'web-app',
    capabilities: [...(scenario.snapshot.capabilities ?? []), 'run_context'],
  },
});

const runData = (api: MockApiHandle) => {
  installRunContextData(api);
  installDiffData(api);
};

async function embed(page: Page) {
  await page.addInitScript(() => {
    window.__ITERVOX_SHELL__ = 'desktop';
  });
}

const path = (page: Page) => new URL(page.url()).pathname;

test.describe('embedded shell', () => {
  test('lands on the issue view, without the sidebar', async ({ page }) => {
    await embed(page);
    await bootApp(page, {
      scenario: withRunContext(activeRunScenario),
      route: '/',
      beforeNavigate: runData,
    });
    await expect(page).toHaveURL(/\/focus$/);
    await expect(page.getByTestId('focus-page')).toBeVisible();
    await expect(page.getByTestId('embedded-title')).toHaveText(/web-app · DEMO-RUN-\d/);
    // No sidebar, no drawer, no menu button.
    await expect(page.getByRole('link', { name: 'Dashboard' })).toHaveCount(0);
    await expect(page.getByRole('dialog', { name: 'Navigation' })).toHaveCount(0);
    await expect(page.getByTestId('mobile-menu-button')).toHaveCount(0);
    await expect(page.getByRole('button', { name: /Commands/ })).toBeVisible();
  });

  test('Board, Sessions and New task from the top bar', async ({ page }) => {
    await embed(page);
    await bootApp(page, { scenario: withRunContext(activeRunScenario), beforeNavigate: runData });
    await expect(page).toHaveURL(/\/focus$/);
    const nav = page.getByRole('navigation', { name: 'Pages' });
    await nav.getByRole('link', { name: 'Board' }).click();
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    expect(path(page)).toBe('/');
    await nav.getByRole('link', { name: 'Sessions' }).click();
    await expect(page).toHaveURL(/\/sessions$/);
    await page.getByRole('button', { name: '+ New task' }).click();
    await expect(page.getByRole('dialog', { name: /New task/ })).toBeVisible();
    expect(path(page)).toBe('/');
  });

  test('the More menu reaches every page', async ({ page }) => {
    await embed(page);
    await bootApp(page, { scenario: withRunContext(activeRunScenario), beforeNavigate: runData });
    await expect(page).toHaveURL(/\/focus$/);
    const more = page.getByRole('button', { name: 'More' });
    await more.click();
    const labels = await page
      .getByRole('menu', { name: 'All pages' })
      .getByRole('menuitem')
      .allTextContents();
    expect(labels).toEqual([
      'Board',
      'Issue view',
      'Sessions',
      'Timeline',
      'Logs',
      'Agents',
      'Automations',
      'Rules',
      'Repo Analysis',
      'Memory',
      'Settings',
    ]);
    await page.keyboard.press('Escape');
    const expected: Record<string, RegExp> = {
      Board: /^\/$/,
      'Issue view': /^\/focus$/,
      Sessions: /^\/sessions$/,
      Timeline: /^\/timeline$/,
      Logs: /^\/logs/,
      Agents: /^\/agents$/,
      Automations: /^\/automations$/,
      Rules: /^\/rules$/,
      'Repo Analysis': /^\/repo$/,
      Memory: /^\/memory$/,
      Settings: /^\/settings$/,
    };
    for (const label of labels) {
      await more.click();
      await page.getByRole('menuitem', { name: label }).click();
      await expect(page.getByRole('menu')).toHaveCount(0);
      await expect.poll(() => path(page), { message: label }).toMatch(expected[label]);
      await expect(page.getByRole('link', { name: 'Dashboard' })).toHaveCount(0);
    }
  });

  test("the desktop app's Go menu navigates through window.__itervoxNavigate", async ({ page }) => {
    await embed(page);
    await bootApp(page, { scenario: withRunContext(activeRunScenario), beforeNavigate: runData });
    await expect(page).toHaveURL(/\/focus$/);
    // What desktop/src-tauri/src/dashboard.rs navigate_script evaluates.
    const ok = await page.evaluate(
      () => window.__itervoxNavigate !== undefined && window.__itervoxNavigate('/agents'),
    );
    expect(ok).toBe(true);
    await expect(page).toHaveURL(/\/agents$/);
    expect(await page.evaluate(() => window.__itervoxNavigate?.('//evil.example/'))).toBe(false);
    await expect(page).toHaveURL(/\/agents$/);
  });

  test('?embedded=1 switches the dev server into embedded mode', async ({ page }) => {
    await bootApp(page, {
      scenario: withRunContext(activeRunScenario),
      route: '/?embedded=1',
      beforeNavigate: runData,
    });
    await expect(page.getByTestId('focus-page')).toBeVisible();
    await expect(page.getByRole('button', { name: 'More' })).toBeVisible();
  });

  test('not embedded: unchanged sidebar and board, and no navigate hook', async ({ page }) => {
    await bootApp(page, { scenario: withRunContext(activeRunScenario), beforeNavigate: runData });
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Dashboard' }).first()).toBeVisible();
    await expect(page.getByRole('button', { name: 'More' })).toHaveCount(0);
    expect(await page.evaluate(() => typeof window.__itervoxNavigate)).toBe('undefined');
  });
});

test.describe('issue view', () => {
  test('/focus picks the issue waiting for input first', async ({ page }) => {
    await bootApp(page, {
      scenario: withRunContext(inputRequiredScenario),
      route: '/focus',
      beforeNavigate: runData,
    });
    await expect(page.getByTestId('focus-page')).toHaveAttribute('data-identifier', 'DEMO-INPUT-1');
    await expect(page.getByText('Agent needs your input')).toBeVisible();
  });

  test('/focus picks the most recently started running issue', async ({ page }) => {
    const scenario = withRunContext(activeRunScenario);
    const [a, b] = scenario.snapshot.running;
    const running = [
      { ...a, startedAt: '2026-10-01T10:00:00Z' },
      { ...b, startedAt: '2026-10-01T11:00:00Z' },
    ];
    await bootApp(page, {
      scenario: { ...scenario, snapshot: { ...scenario.snapshot, running } },
      route: '/focus',
      beforeNavigate: runData,
    });
    await expect(page.getByTestId('focus-page')).toHaveAttribute('data-identifier', b.identifier);
    await expect(page.getByTestId('focus-last-tool')).toContainText('Bash');
    await expect(page.getByTestId('issue-steer-box')).toBeVisible();
    // The Changes column is the shared diff viewer.
    const changes = page.getByTestId('focus-changes');
    await changes
      .getByRole('button', { name: /greeting\.go/ })
      .first()
      .click();
    await expect(changes.getByText('return legacyGreeting()')).toBeVisible();
  });

  test('/focus shows the empty state when there is nothing to show', async ({ page }) => {
    await bootApp(page, { scenario: withRunContext(emptyScenario), route: '/focus' });
    await expect(page.getByTestId('focus-empty')).toContainText('Nothing to focus on yet');
    await page.getByRole('link', { name: 'Open the board' }).click();
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
  });

  test('the issue detail opens the full view', async ({ page }) => {
    await bootApp(page, {
      scenario: withRunContext(activeRunScenario),
      route: '/?issue=DEMO-RUN-1',
      beforeNavigate: runData,
    });
    const slide = page.getByRole('dialog', { name: 'DEMO-RUN-1' });
    await slide.getByRole('link', { name: 'Open full view' }).click();
    await expect(page).toHaveURL(/\/focus\/DEMO-RUN-1$/);
    await expect(page.getByTestId('focus-page')).toHaveAttribute('data-identifier', 'DEMO-RUN-1');
    await expect(page.getByRole('dialog', { name: 'DEMO-RUN-1' })).toHaveCount(0);
  });

  test('phone width: the two columns stack', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await bootApp(page, {
      scenario: withRunContext(activeRunScenario),
      route: '/focus/DEMO-RUN-1',
      beforeNavigate: runData,
    });
    const now = await page.getByTestId('focus-now').boundingBox();
    const changes = await page.getByTestId('focus-changes').boundingBox();
    expect(now && changes && changes.y >= now.y + now.height).toBe(true);
    const { scrollWidth, innerWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
    }));
    expect(scrollWidth).toBeLessThanOrEqual(innerWidth);
  });
});
