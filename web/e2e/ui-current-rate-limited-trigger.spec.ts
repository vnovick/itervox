// Gap §4.2 — visual smoke for the new "Rate Limited" trigger option in
// the automation editor. Mirrors ui-current-pr-opened-option.spec.ts.
// Boots /automations, opens the Add Automation modal, selects "Rate
// Limited" from the trigger dropdown, and asserts the conditional
// switch-fields block (switch_to_profile, switch_to_backend,
// cooldown_minutes, auto-switch toggle) renders.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { settingsMatrixScenario } from './fixtures/scenarios';

test.describe('Rate Limited trigger option (gap E)', () => {
  test('appears in the trigger dropdown and reveals switch fields when selected', async ({
    page,
  }) => {
    await bootApp(page, { scenario: settingsMatrixScenario, route: '/automations' });

    await expect(page.getByRole('heading', { name: 'Automations', level: 1 })).toBeVisible();
    await page.getByRole('button', { name: /add automation/i }).click();
    await expect(page.getByRole('heading', { name: /add automation/i })).toBeVisible();

    const triggerSelect = page.locator('select').filter({ hasText: /Cron|Rate Limited/i }).first();
    await expect(triggerSelect).toContainText('Rate Limited');

    await triggerSelect.selectOption('rate_limited');
    await expect(
      page.getByText(
        /Fires when an exhausted-retry exit was caused by vendor rate-limit/i,
      ),
    ).toBeVisible();

    // Conditional fields render only for rate_limited triggers.
    await expect(page.getByLabel(/Switch to profile/i)).toBeVisible();
    await expect(page.getByLabel(/Override backend/i)).toBeVisible();
    await expect(page.getByLabel(/Cooldown \(minutes\)/i)).toBeVisible();
    await expect(page.getByText(/Auto-switch \(no human in the loop\)/i)).toBeVisible();

    await page.screenshot({
      path: 'test-results/rate-limited-trigger.png',
      fullPage: false,
    });
  });
});
