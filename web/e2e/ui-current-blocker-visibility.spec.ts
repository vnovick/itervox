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

    await page.getByText('Blocked issue', { exact: true }).click();

    await expect(page.getByRole('heading', { name: 'Blocked by' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Not dispatchable' })).toBeVisible();
    await expect(page.getByText('blocked by DEMO-1', { exact: true })).toBeVisible();

    const blockerLink = page.getByRole('link', { name: 'DEMO-1' });
    await expect(blockerLink).toBeVisible();
    await expect(blockerLink).toHaveAttribute('href', 'https://example.com/issues/DEMO-1');
    await expect(page.getByText('In Progress', { exact: true }).last()).toBeVisible();
  });
});
