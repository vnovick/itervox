// T-72 — Real-daemon dashboard-boot + state-schema smoke (Lane 3).
//
// Boots the real itervox binary against the embedded quickstart WORKFLOW.md,
// navigates the dashboard with a URL token, and asserts:
//   1. Dashboard renders within 5s (h1 "Autonomous agentic harness").
//   2. AppHeader transitions from "Connecting…" to "Live" within 3s of snapshot.
//   3. /api/v1/state shape round-trips through StateSnapshotSchema.parse — the
//      single catch-all for Go ↔ TypeScript schema drift.

import { expect, test } from '@playwright/test';
import { startDaemon, type Daemon } from './helpers/daemon';

let daemon: Daemon;

test.beforeAll(async () => {
  daemon = await startDaemon();
});

test.afterAll(async () => {
  await daemon.stop();
});

test('dashboard renders + SSE goes Live + /state schema-validates', async ({ page }) => {
  await page.goto(`${daemon.url}/?token=${encodeURIComponent(daemon.token)}`);

  await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible({
    timeout: 5_000,
  });

  // SSE indicator transitions to Live once first snapshot arrives.
  await expect(page.getByText(/^Live$/)).toBeVisible({ timeout: 6_000 });

  // Schema-validate /state using the production bundle's validation hook.
  // Fetches with the same Authorization header the dashboard uses.
  const validation = await page.evaluate(async (token) => {
    const res = await fetch('/api/v1/state', {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!res.ok) {
      return { ok: false, status: res.status, error: 'fetch_failed' };
    }
    const json = await res.json();
    const validator = (
      window as Window & {
        __ITERVOX_VALIDATE_STATE__?: (
          snapshot: unknown,
        ) => { ok: true } | { ok: false; error: string };
      }
    ).__ITERVOX_VALIDATE_STATE__;
    if (typeof validator !== 'function') {
      return { ok: false, error: 'production_state_validator_missing' };
    }
    return validator(json);
  }, daemon.token);

  expect(validation.ok, `schema validation failed: ${JSON.stringify(validation)}`).toBe(true);
});

test('/api/v1/health returns 200 with bearer token', async () => {
  const res = await fetch(`${daemon.url}/api/v1/health`, {
    headers: { Authorization: `Bearer ${daemon.token}` },
  });
  expect(res.status).toBe(200);
});
