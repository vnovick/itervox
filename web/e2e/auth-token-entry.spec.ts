import { test, expect, type Page } from '@playwright/test';
import { startDaemon, type Daemon } from './helpers/daemon';

/**
 * Flow 1 (T-31): auth-token-entry.
 *
 * The daemon requires a bearer token on every bind, loopback included. The
 * helper sets ITERVOX_API_TOKEN, then we load the dashboard WITHOUT the
 * `?token=` query param — the AuthGate should block the app and render the
 * token entry form. After typing the right token and submitting, the
 * dashboard renders.
 */
let daemon: Daemon;

test.beforeAll(async () => {
  daemon = await startDaemon();
});

test.afterAll(async () => {
  await daemon.stop();
});

// The API only accepts `Authorization: Bearer`, so a bare fetch is always
// 401. Use the token AuthGate persisted (same lookup order as tokenStore) to
// prove the app stored the right one and that it authorizes the API.
async function expectStateApiOK(page: Page, expectedToken: string) {
  const { stored, status } = await page.evaluate(async () => {
    const token =
      localStorage.getItem('itervox.apiToken.persistent') ??
      sessionStorage.getItem('itervox.apiToken');
    const res = await fetch('/api/v1/state', {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    return { stored: token, status: res.status };
  });
  expect(stored).toBe(expectedToken);
  expect(status).toBe(200);
}

test('shows token entry screen when no token is stored, accepts the right token', async ({
  page,
}) => {
  // No token in storage; load the dashboard root without ?token=.
  await page.goto(daemon.url);

  // The token entry form should be visible. We match by role/text rather than
  // CSS classes to be resilient to styling changes.
  await expect(page.getByRole('heading', { name: /token|sign in|enter/i })).toBeVisible();

  const tokenInput = page.getByRole('textbox').first();
  await tokenInput.fill(daemon.token);

  await page.getByRole('button', { name: /sign in|continue|submit|enter/i }).click();

  // After submission the dashboard's project name or live label should appear.
  // We match `Live` (sse status text) which the AppHeader always renders once
  // the snapshot loads.
  await expect(page.getByText(/^Live$/)).toBeVisible({ timeout: 10_000 });
  await expect(page.getByRole('heading', { name: /token|sign in|enter/i })).toHaveCount(0);
  await expectStateApiOK(page, daemon.token);
});

test('rejects an obviously wrong token', async ({ page }) => {
  await page.goto(daemon.url);

  await expect(page.getByRole('heading', { name: /token|sign in|enter/i })).toBeVisible();
  await page.getByRole('textbox').first().fill('not-the-real-token');
  await page.getByRole('button', { name: /sign in|continue|submit|enter/i }).click();

  // Either the form re-renders with an error, OR the gate stays on the entry
  // screen. The dashboard should NOT load.
  // Give the auth probe a beat to finish.
  await page.waitForTimeout(500);
  await expect(page.getByRole('heading', { name: /token|sign in|enter/i })).toBeVisible();
});
