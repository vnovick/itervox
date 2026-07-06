// T-71 — Auth use-case matrix (route-mocked).
//
// Extends T-62's boot-smoke cases with mid-session transitions and recovery
// paths: 401 mid-session, server-down recovery, token-store cross-state
// behavior. Heavyweight cross-tab cases (storage event sync, token rotation
// queue replay) are documented but deferred — they require multi-context
// coordination and are tracked separately.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { installMockApi } from './fixtures/mockApi';
import { installMockSse } from './fixtures/mockSse';
import { E2E_TOKEN, quickstartScenario } from './fixtures/scenarios';

const SESSION_KEY = 'itervox.apiToken';
const LOCAL_KEY = 'itervox.apiToken.persistent';

test.describe('T-71 auth matrix', () => {
  test('1. Healthy server with no token → token-entry screen', async ({ page }) => {
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario);
    api.override(/\/api\/v1\/state$/, async (route) => {
      const auth = (await route.request().allHeaders())['authorization'];
      if (!auth) {
        return route.fulfill({ status: 401, contentType: 'application/json', body: '{}' });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(quickstartScenario.snapshot),
      });
    });
    await page.goto('/');
    await expect(page.getByText(/Sign in to Itervox/)).toBeVisible();
  });

  test('2. URL token wins over a stale stored token', async ({ page, context }) => {
    // Prime sessionStorage with a stale value, then boot with a different ?token=.
    await context.addInitScript((args) => {
      try { sessionStorage.setItem(args.key, args.stale); } catch (_) { /* ignore */ }
    }, { key: SESSION_KEY, stale: 'stale-token' });

    await bootApp(page, { scenario: quickstartScenario, token: E2E_TOKEN });
    expect(await page.evaluate((k) => sessionStorage.getItem(k), SESSION_KEY)).toBe(E2E_TOKEN);
  });

  test('3. Stored token in sessionStorage authorizes without prompt', async ({ page, context }) => {
    await context.addInitScript((args) => {
      try { sessionStorage.setItem(args.key, args.token); } catch (_) { /* ignore */ }
    }, { key: SESSION_KEY, token: E2E_TOKEN });

    // Boot WITHOUT a URL token — the stored one should be used by AuthGate.
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario);
    api.override(/\/api\/v1\/state$/, async (route) => {
      const auth = (await route.request().allHeaders())['authorization'];
      if (auth !== `Bearer ${E2E_TOKEN}`) {
        return route.fulfill({ status: 401, contentType: 'application/json', body: '{}' });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(quickstartScenario.snapshot),
      });
    });
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    await expect(page.getByText(/Sign in to Itervox/)).toHaveCount(0);
  });

  // Server-down auto-recovery: the first /health probe returns 503 (renders the
  // ServerDownScreen); clicking Retry re-runs probe(), this time the override
  // succeeds and the dashboard mounts.
  //
  // We toggle the override response based on a `healthy` flag (not a hit
  // counter) because React 19 strict mode runs effects twice on mount, so the
  // initial probe may fire two health requests before the operator clicks
  // Retry. A counter-based gate would race that strict-mode behaviour; a
  // flag is order-independent.
  test('4. Server-down auto-recovery: Retry button re-probes /health', async ({ page }) => {
    const api = await installMockApi(page, quickstartScenario);

    let healthy = false;
    api.override(/\/api\/v1\/health(\?|$)/, async (route) => {
      if (!healthy) {
        return route.fulfill({ status: 503, contentType: 'text/plain', body: 'down' });
      }
      return route.fulfill({ status: 200, contentType: 'text/plain', body: 'ok' });
    });

    await installMockSse(page, quickstartScenario);

    await page.goto(`/?token=${encodeURIComponent(E2E_TOKEN)}`);
    await expect(page.getByText(/Can't reach the daemon/)).toBeVisible({ timeout: 10_000 });

    // Flip the daemon to "up" before retrying so the next probe succeeds.
    healthy = true;
    await page.getByRole('button', { name: 'Retry' }).click();
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible({
      timeout: 10_000,
    });
  });

  test('5. "Remember me" → token in localStorage; reload still authorized', async ({ page }) => {
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario);
    api.override(/\/api\/v1\/state$/, async (route) => {
      const auth = (await route.request().allHeaders())['authorization'];
      if (auth !== `Bearer ${E2E_TOKEN}`) {
        return route.fulfill({ status: 401, contentType: 'application/json', body: '{}' });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(quickstartScenario.snapshot),
      });
    });

    await page.goto('/');
    await expect(page.getByText(/Sign in to Itervox/)).toBeVisible();
    await page.locator('#api-token').fill(E2E_TOKEN);
    await page.getByRole('checkbox').check();
    await page.getByRole('button', { name: /Sign in/ }).click();

    // After submit, dashboard should render and token should be persistent.
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    expect(await page.evaluate((k) => localStorage.getItem(k), LOCAL_KEY)).toBe(E2E_TOKEN);

    // Reload — still authorized.
    await page.reload();
    await expect(page.getByText(/Sign in to Itervox/)).toHaveCount(0);
    expect(await page.evaluate((k) => localStorage.getItem(k), LOCAL_KEY)).toBe(E2E_TOKEN);
  });

  test('6. /state returns 500 → server-down', async ({ page }) => {
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario);
    api.override(/\/api\/v1\/state$/, async (route) => {
      return route.fulfill({ status: 500, contentType: 'application/json', body: '{}' });
    });
    await page.goto(`/?token=${encodeURIComponent(E2E_TOKEN)}`);
    await expect(page.getByText(/Can't reach the daemon/)).toBeVisible();
  });
});
