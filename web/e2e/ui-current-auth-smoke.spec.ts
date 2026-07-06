// T-62 — Auth + boot smoke (route-mocked).
//
// Locks the AuthGate state machine: ?token capture, sessionStorage vs
// localStorage persistence, 401 → token-entry, network error → server-down.
//
// Every other route depends on the gate working, so this is the highest-leverage
// smoke spec in Lane 2.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { installMockApi } from './fixtures/mockApi';
import { installMockSse } from './fixtures/mockSse';
import { E2E_TOKEN, quickstartScenario } from './fixtures/scenarios';

const SESSION_KEY = 'itervox.apiToken';
const LOCAL_KEY = 'itervox.apiToken.persistent';

test.describe('T-62 auth smoke', () => {
  test('1. URL token captured, stripped, persisted in sessionStorage', async ({ page }) => {
    await bootApp(page, { scenario: quickstartScenario, token: E2E_TOKEN });

    // URL stripped via history.replaceState
    expect(new URL(page.url()).search).toBe('');

    // Token in sessionStorage, NOT localStorage
    const session = await page.evaluate((k) => sessionStorage.getItem(k), SESSION_KEY);
    expect(session).toBe(E2E_TOKEN);
    const local = await page.evaluate((k) => localStorage.getItem(k), LOCAL_KEY);
    expect(local).toBeNull();
  });

  test('2. Stored token survives reload (no re-prompt)', async ({ page }) => {
    await bootApp(page, { scenario: quickstartScenario, token: E2E_TOKEN });
    expect(await page.evaluate((k) => sessionStorage.getItem(k), SESSION_KEY)).toBe(E2E_TOKEN);

    await page.reload();

    // Still authorized — token-entry screen should NOT appear.
    await expect(page.getByText(/Sign in to Itervox/)).toHaveCount(0);
    expect(await page.evaluate((k) => sessionStorage.getItem(k), SESSION_KEY)).toBe(E2E_TOKEN);
  });

  test('3. Missing token → token-entry, submit unlocks', async ({ page }) => {
    // Fresh boot, no token → /state will be 401 because no Authorization header.
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario);
    api.override(/\/api\/v1\/state$/, async (route) => {
      const headers = await route.request().allHeaders();
      const auth = headers['authorization'];
      if (!auth || auth !== `Bearer ${E2E_TOKEN}`) {
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
    await page.getByRole('button', { name: /Sign in/ }).click();

    // After submission, token should be set + status authorized → children render.
    await expect(page.getByText(/Sign in to Itervox/)).toHaveCount(0);
  });

  test('4. Wrong token → 401 → token cleared, token-entry returns', async ({ page }) => {
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario);
    api.override(/\/api\/v1\/state$/, async (route) => {
      // Reject every token.
      return route.fulfill({ status: 401, contentType: 'application/json', body: '{}' });
    });

    await page.goto(`/?token=${encodeURIComponent('wrong-token')}`);

    // AuthGate sees 401 → clears token + needsToken status.
    await expect(page.getByText(/Sign in to Itervox/)).toBeVisible();
    expect(await page.evaluate((k) => sessionStorage.getItem(k), SESSION_KEY)).toBeNull();
    expect(await page.evaluate((k) => localStorage.getItem(k), LOCAL_KEY)).toBeNull();
  });

  test('5. /health network error → server-down screen', async ({ page }) => {
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario);
    api.override(/\/api\/v1\/health$/, async (route) => {
      return route.abort('failed');
    });

    await page.goto(`/?token=${encodeURIComponent(E2E_TOKEN)}`);
    await expect(page.getByText(/Can't reach the daemon/)).toBeVisible();
  });

  test('6. /health 401 → server-down screen (auth required at health level)', async ({ page }) => {
    // AuthGate path: when /health returns non-OK status, AuthGate sets serverDown
    // (per AuthGate.tsx:41-44). 401 is non-OK so this should land on serverDown.
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario);
    api.override(/\/api\/v1\/health$/, async (route) => {
      return route.fulfill({ status: 401, contentType: 'application/json', body: '{}' });
    });

    await page.goto(`/?token=${encodeURIComponent(E2E_TOKEN)}`);
    await expect(page.getByText(/Can't reach the daemon/)).toBeVisible();
  });

  test('7. "Remember me" writes to localStorage, not sessionStorage', async ({ page }) => {
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario);
    api.override(/\/api\/v1\/state$/, async (route) => {
      const headers = await route.request().allHeaders();
      const auth = headers['authorization'];
      if (!auth || auth !== `Bearer ${E2E_TOKEN}`) {
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
    await page.getByRole('checkbox').check(); // "Remember on this device"
    await page.getByRole('button', { name: /Sign in/ }).click();

    await expect(page.getByText(/Sign in to Itervox/)).toHaveCount(0);

    // Token in localStorage (persistent), session cleared.
    expect(await page.evaluate((k) => localStorage.getItem(k), LOCAL_KEY)).toBe(E2E_TOKEN);
    expect(await page.evaluate((k) => sessionStorage.getItem(k), SESSION_KEY)).toBeNull();
  });
});
