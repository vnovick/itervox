import { test, expect, type Page } from '@playwright/test';
import { startDaemon, type Daemon } from './helpers/daemon';

/**
 * Flow 2 (T-31): auth-url-token.
 *
 * AuthGate captures `?token=…` from the URL on first load, persists it in
 * sessionStorage, and strips the query param via history.replaceState so it
 * doesn't show up in the address bar after a refresh.
 */
let daemon: Daemon;

test.beforeAll(async () => {
  daemon = await startDaemon();
});

test.afterAll(async () => {
  await daemon.stop();
});

async function expectStateApiOK(page: Page) {
  const status = await page.evaluate(async () => {
    const res = await fetch('/api/v1/state');
    return res.status;
  });
  expect(status).toBe(200);
}

test('captures ?token= from the URL and renders the dashboard immediately', async ({ page }) => {
  await page.goto(`${daemon.url}/?token=${daemon.token}`);

  // Dashboard should load straight to its main content (no token-entry form).
  await expect(page.getByText(/^Live$/)).toBeVisible({ timeout: 10_000 });
  // The token entry heading should NOT appear.
  await expect(page.getByRole('heading', { name: /enter your itervox token/i })).toHaveCount(0);
  await expectStateApiOK(page);

  // The query param should be stripped from the URL.
  await expect.poll(() => new URL(page.url()).searchParams.get('token')).toBeNull();
});

test('a stored token survives a page reload (sessionStorage)', async ({ page }) => {
  await page.goto(`${daemon.url}/?token=${daemon.token}`);
  await expect(page.getByText(/^Live$/)).toBeVisible({ timeout: 10_000 });

  // Reload — without the query param, AuthGate must read from sessionStorage.
  await page.reload();
  await expect(page.getByText(/^Live$/)).toBeVisible({ timeout: 10_000 });
  await expectStateApiOK(page);
});
