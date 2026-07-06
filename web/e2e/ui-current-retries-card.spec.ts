// G — visual smoke for the new Retries card on /settings.
//
// Boots the app with the settings-matrix scenario, navigates to /settings,
// confirms both new controls render: the "Max retries per issue" numeric
// input and the "On exhausted retries" select with the "Pause (do not move)"
// option plus per-known-state options.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { settingsMatrixScenario } from './fixtures/scenarios';

test.describe('Retries card (gap G)', () => {
  test('renders max_retries input + failed_state select with Pause option', async ({ page }) => {
    await bootApp(page, { scenario: settingsMatrixScenario, route: '/settings' });

    await expect(page.getByRole('heading', { name: 'Settings', level: 1 })).toBeVisible();

    // Both the section and the card render an h2 with text "Retries" — matching
    // either by role+name returns multiple. Use first(); the visibility of the
    // form controls below is the real assertion.
    await expect(page.getByRole('heading', { name: /^Retries$/i, level: 2 }).first()).toBeVisible();

    // Max retries: server-side default is 5 — fixture inherits that.
    const maxRetriesInput = page.getByLabel('Max retries per issue');
    await expect(maxRetriesInput).toBeVisible();
    await expect(maxRetriesInput).toHaveValue('5');

    // Failed-state select includes the "Pause (do not move)" option as the
    // empty value, plus per-known-state options derived from
    // active/terminal/backlog. The configured completionState ("Done") is
    // intentionally excluded — auto-routing exhausted runs to a
    // success-on-failure state would be a foot-gun.
    const select = page.getByLabel('On exhausted retries');
    await expect(select).toBeVisible();
    await expect(select).toContainText('Pause (do not move)');
    await expect(select.locator('option[value="In Progress"]')).toHaveCount(1);
    await expect(select.locator('option[value="Backlog"]')).toHaveCount(1);
    await expect(select.locator('option[value="Done"]')).toHaveCount(0);

    await page.screenshot({
      path: 'test-results/retries-card.png',
      fullPage: false,
    });
  });
});
