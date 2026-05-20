import { test, expect } from '@playwright/test';
import { startDaemon, type Daemon } from './helpers/daemon';

/**
 * Flow 3 (T-31): automation-crud.
 *
 * Drives the Automations settings card through a create + delete cycle. The
 * quickstart template ships with no automations and one default profile
 * (`echo`), so the form should accept a new automation that references
 * `echo` and persist it via PUT /api/v1/settings/automations.
 *
 * The test is intentionally lightweight: it asserts the full round-trip
 * (form fill → server persist → snapshot reflects new automation → row
 * deletion → snapshot reflects empty list). It does NOT exercise every
 * trigger type — those have unit-level coverage.
 */
let daemon: Daemon;

test.beforeAll(async () => {
  daemon = await startDaemon();
});

test.afterAll(async () => {
  await daemon.stop();
});

test('create + delete an automation round-trip', async ({ page }) => {
  // Authenticate via URL token (proven by flow 2).
  await page.goto(`${daemon.url}/?token=${daemon.token}`);
  await expect(page.getByText(/^Live$/)).toBeVisible({ timeout: 10_000 });

  // Navigate to the settings/automations page. The Automations link is in the
  // app sidebar; if the layout changes, this selector will need updating.
  await page.goto(`${daemon.url}/automations`);
  await expect(page.getByText(/Automations/i).first()).toBeVisible();

  // Open the "Add Automation" modal.
  await page.getByRole('button', { name: /add automation/i }).click();
  await expect(page.getByRole('heading', { name: /add automation/i })).toBeVisible();

  // Fill in a minimal cron automation. The id input is autoFocused.
  await page.getByLabel(/automation id/i).fill('e2e-smoke');

  // The cron picker default emits `0 9 * * 1-5` (Mon-Fri at 9am) when
  // humanizeValue=false is set in CronPicker — that's valid Unix cron.
  // No need to override the cron field here.

  // Submit — the form's submit button is labelled "Create Automation".
  await page.getByRole('button', { name: /create automation/i }).click();

  // After the round-trip the modal closes and the new automation appears in
  // the list. Use polling because the SSE snapshot may take a tick.
  await expect
    .poll(
      async () => {
        return page.getByText('e2e-smoke').isVisible();
      },
      { timeout: 5_000 },
    )
    .toBe(true);

  // Delete the automation — find a delete button on the new row.
  // Use the stable data-automation-row attribute as the anchor; ancestor div
  // selectors may match high up in the tree where the button isn't a direct
  // child, leading to "first()" picking a wrong element.
  const row = page.locator('[data-automation-row="e2e-smoke"]');
  await row.getByRole('button', { name: /delete|remove/i }).click();

  // After delete, the row should be gone from the snapshot.
  await expect
    .poll(
      async () => {
        return page.getByText('e2e-smoke').count();
      },
      { timeout: 5_000 },
    )
    .toBe(0);
});
