// M6-W1 — URL-addressable Logs selection and Automations tab (CORE-085) and
// clickable header pills (CORE-086), route-mocked.

import { expect, test, type Page, type Route } from '@playwright/test';
import { installMockApi, type MockApiHandle } from './fixtures/mockApi';
import { installMockSse } from './fixtures/mockSse';
import {
  activeRunScenario,
  E2E_TOKEN,
  quickstartScenario,
  retryAndPausedScenario,
  type Scenario,
} from './fixtures/scenarios';

/** bootApp appends ?token= to the route, which breaks routes with a query. */
async function bootAt(page: Page, scenario: Scenario, path: string): Promise<MockApiHandle> {
  const api = await installMockApi(page, scenario);
  await installMockSse(page, scenario, 'one-shot');
  const sep = path.includes('?') ? '&' : '?';
  await page.goto(`${path}${sep}token=${encodeURIComponent(E2E_TOKEN)}`);
  return api;
}

function delayedJson(body: unknown, ms: number) {
  return async (route: Route) => {
    await new Promise((r) => setTimeout(r, ms));
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(body),
    });
  };
}

/** Records every history.pushState/replaceState URL into window.__urls. */
async function recordHistory(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as { __urls: string[] };
    w.__urls = [];
    for (const name of ['pushState', 'replaceState'] as const) {
      const orig = history[name].bind(history);
      history[name] = (data: unknown, unused: string, url?: string | URL | null) => {
        if (url) w.__urls.push(new URL(String(url), location.href).pathname);
        orig(data, unused, url);
      };
    }
  });
}
const historyUrls = (page: Page) =>
  page.evaluate(() => (window as unknown as { __urls: string[] }).__urls);

/** The Logs title bar shows the selected identifier (green) next to its state. */
const selectedTitle = (page: Page, id: string) =>
  page.getByTestId('logs-selected-id').filter({ hasText: new RegExp(`^${id}$`) });

test.describe('CORE-085 URL-addressable Logs selection', () => {
  test('logs deep link selects issue', async ({ page }) => {
    await bootAt(page, quickstartScenario, '/logs/DEMO-2');
    await expect(page).toHaveURL(/\/logs\/DEMO-2$/);
    await expect(selectedTitle(page, 'DEMO-2')).toBeVisible();
    await expect(page.getByTestId('logs-context-strip')).toBeVisible();
    // Reload keeps the selection (it lives in the path, not the store).
    await page.reload();
    await expect(page).toHaveURL(/\/logs\/DEMO-2$/);
    await expect(selectedTitle(page, 'DEMO-2')).toBeVisible();
    // Choosing another issue pushes; Back returns to DEMO-2.
    await page.getByRole('button', { name: 'DEMO-3', exact: true }).click();
    await expect(page).toHaveURL(/\/logs\/DEMO-3$/);
    await page.goBack();
    await expect(page).toHaveURL(/\/logs\/DEMO-2$/);
    await expect(selectedTitle(page, 'DEMO-2')).toBeVisible();
  });

  test('logs deep link survives initial issue-list load', async ({ page }) => {
    // DEMO-RUN-1 is absent from the first snapshot and present in the second;
    // the issue list and log identifiers (which never contain it) are slow.
    const t0 = Date.now();
    const withoutRun = { ...activeRunScenario.snapshot, running: [] };
    const snapshotNow = () => (Date.now() - t0 > 900 ? activeRunScenario.snapshot : withoutRun);
    const api = await installMockApi(page, activeRunScenario);
    api.override(/\/api\/v1\/issues$/, delayedJson([], 3000));
    api.override(/\/api\/v1\/logs\/identifiers$/, delayedJson([], 3000));
    api.override(/\/api\/v1\/state$/, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(snapshotNow()),
      });
    });
    await page.route('**/api/v1/events', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: `event: snapshot\ndata: ${JSON.stringify(snapshotNow())}\n\n`,
      });
    });
    await recordHistory(page);
    await page.goto(`/logs/DEMO-RUN-1?token=${encodeURIComponent(E2E_TOKEN)}`);
    await page.waitForTimeout(600);
    await expect(page).toHaveURL(/\/logs\/DEMO-RUN-1$/);
    // After everything has settled the id is in the running rows: kept.
    await page.waitForTimeout(3200);
    await expect(page).toHaveURL(/\/logs\/DEMO-RUN-1$/);
    await expect(selectedTitle(page, 'DEMO-RUN-1')).toBeVisible();
    // Never navigated away from the deep link, not even for a moment.
    expect(new Set(await historyUrls(page))).toEqual(new Set(['/logs/DEMO-RUN-1']));
  });

  test('logs deep link to an unknown identifier falls back to the first visible issue only after issues and snapshot have loaded', async ({
    page,
  }) => {
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario, 'one-shot');
    api.override(/\/api\/v1\/issues$/, delayedJson(quickstartScenario.issues, 1500));
    await page.goto(`/logs/NOPE-404?token=${encodeURIComponent(E2E_TOKEN)}`);
    // Still loading: the deep link is not overwritten by the auto-select.
    await page.waitForTimeout(700);
    await expect(page).toHaveURL(/\/logs\/NOPE-404$/);
    // Settled without it: replaced by the first sidebar row.
    await expect(page).toHaveURL(/\/logs\/DEMO-1$/, { timeout: 5_000 });
    await expect(selectedTitle(page, 'DEMO-1')).toBeVisible();
    // The fallback replaced the entry: Back leaves /logs entirely.
    await page.goBack();
    await expect(page).not.toHaveURL(/\/logs\/NOPE-404/);
  });

  test('automations ?tab= restores the tab on reload and Back', async ({ page }) => {
    await bootAt(page, quickstartScenario, '/automations?openAutomation=keep-me');
    const activity = page.getByRole('tab', { name: 'Activity' });
    const configure = page.getByRole('tab', { name: 'Configure' });
    await expect(configure).toHaveAttribute('aria-selected', 'true');
    await activity.click();
    await expect(page).toHaveURL(/[?&]tab=activity/);
    await expect(page).toHaveURL(/[?&]openAutomation=keep-me/);
    await page.reload();
    await expect(page.getByRole('tab', { name: 'Activity' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await page.getByRole('tab', { name: 'Configure' }).click();
    await expect(page).not.toHaveURL(/tab=/);
    await page.goBack();
    await expect(page).toHaveURL(/[?&]tab=activity/);
    await expect(page.getByRole('tab', { name: 'Activity' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });
});

test.describe('CORE-086 clickable header pills', () => {
  test('the retrying pill jumps to the retry queue', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 600 });
    await bootAt(page, retryAndPausedScenario, '/logs');
    await page.getByRole('link', { name: /1 retrying/ }).click();
    await expect(page).toHaveURL(/\/#retry-queue$/);
    await expect(page.locator('#retry-queue')).toBeInViewport();
  });
});
