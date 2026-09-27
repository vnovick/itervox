import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { retryAndPausedScenario, type Scenario } from './fixtures/scenarios';

const blockerVisibilityScenario: Scenario = {
  ...retryAndPausedScenario,
  issues: retryAndPausedScenario.issues.map((issue) =>
    issue.identifier === 'DEMO-BLOCKED-1' ? { ...issue, state: 'Backlog' } : issue,
  ),
};

test.describe('blocker visibility', () => {
  test('blocked issue shows contributor-visible dependency details', async ({ page }) => {
    await bootApp(page, { scenario: blockerVisibilityScenario });

    await expect(page.getByText('Blocked 1', { exact: true }).first()).toBeVisible();
    // CORE-080 — the card carries the why-idle chip.
    await expect(page.getByTestId('why-idle-chip').first()).toContainText('Blocked by DEMO-1');

    await page.getByText('Blocked issue', { exact: true }).click();

    await expect(page.getByRole('heading', { name: 'Blocked by' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Not dispatchable' })).toBeVisible();
    // CORE-080 — the shared label table, with the machine reason beside it.
    const dialog = page.getByRole('dialog', { name: 'DEMO-BLOCKED-1' });
    await expect(dialog.getByTitle('blocked_by:DEMO-1')).toContainText('Blocked by DEMO-1');
    await expect(dialog.getByText('(blocked_by:DEMO-1)', { exact: true })).toBeVisible();

    const blockerLink = page.getByRole('link', { name: 'DEMO-1' });
    await expect(blockerLink).toBeVisible();
    await expect(blockerLink).toHaveAttribute('href', 'https://example.com/issues/DEMO-1');
    await expect(page.getByText('In Progress', { exact: true }).last()).toBeVisible();
  });
});
