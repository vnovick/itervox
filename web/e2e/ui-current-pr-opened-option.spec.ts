// Visual smoke for gap B — the new "PR Opened" trigger option.
//
// Boots /automations, opens the Add Automation modal, shows the trigger
// dropdown, and screenshots the result for human review.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { settingsMatrixScenario } from './fixtures/scenarios';

test.describe('PR Opened trigger option (gap B)', () => {
  test('appears in the trigger-type dropdown of the Add Automation modal', async ({ page }) => {
    await bootApp(page, { scenario: settingsMatrixScenario, route: '/automations' });

    await expect(page.getByRole('heading', { name: 'Automations', level: 1 })).toBeVisible();
    await page.getByRole('button', { name: /add automation/i }).click();
    await expect(page.getByRole('heading', { name: /add automation/i })).toBeVisible();

    const triggerSelect = page.locator('select').filter({ hasText: /Cron|PR Opened/i }).first();
    await expect(triggerSelect).toContainText('PR Opened');

    await triggerSelect.selectOption('pr_opened');
    await expect(page.getByText(/Fires the moment a worker confirms a brand-new pull request/i)).toBeVisible();

    await page.screenshot({
      path: 'test-results/pr-opened-trigger-option.png',
      fullPage: false,
    });
  });
});
