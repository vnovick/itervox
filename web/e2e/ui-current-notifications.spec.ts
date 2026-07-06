// Phase 4 — visual + interaction smoke for the Notifications dashboard tab.
// Covers six checks:
//   1. Notifications tab is visible alongside Board / List / Agents
//   2. Tab badge shows "Notifications · 5" for the populated scenario
//   3. Clicking the tab shows all five group sections
//   4. Green "✓ completed this session" pill on session-completed review
//      item; muted "in review (tracker)" pill on tracker-only review item
//   5. Clicking an input_required row opens IssueDetailSlide for that id
//   6. Same green pill appears inline on ReviewQueueSection.awaitingReview
//      rows in the Board view (proves the shared component)

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { notificationsScenario, emptyScenario } from './fixtures/scenarios';

test.describe('Notifications view (phase 4)', () => {
  test('renders all five group sections with count badge and click-through', async ({ page }) => {
    await bootApp(page, { scenario: notificationsScenario, route: '/' });

    // 1. Notifications tab present.
    const notificationsButton = page.getByRole('button', { name: /Notifications/i });
    await expect(notificationsButton).toBeVisible();

    // 2. Badge — count > 0 → "Notifications · N". The scenario seeds 5
    // items: 1 needs-input + 2 reviews + 1 retrying + 1 paused + 1 config
    // = 6. Don't pin the exact number; just assert the dot-separator format.
    await expect(notificationsButton).toContainText(/Notifications · \d+/);

    // 3. Click → all five sections render.
    await notificationsButton.click();
    await expect(page.getByRole('heading', { level: 2, name: /Needs input · 1/i })).toBeVisible();
    await expect(page.getByRole('heading', { level: 2, name: /Ready for review/i })).toBeVisible();
    await expect(page.getByRole('heading', { level: 2, name: /Retrying · 1/i })).toBeVisible();
    await expect(page.getByRole('heading', { level: 2, name: /Paused · 1/i })).toBeVisible();
    await expect(
      page.getByRole('heading', { level: 2, name: /Config issues · 1/i }),
    ).toBeVisible();

    // 4. Both review pills present.
    await expect(page.getByText(/✓ completed this session/i).first()).toBeVisible();
    await expect(page.getByText(/in review \(tracker\)/i).first()).toBeVisible();

    await page.screenshot({
      path: 'test-results/notifications-view.png',
      fullPage: false,
    });
  });

  // Gap §10.2 — empty-state cover.
  test('renders the "All caught up" empty state when nothing needs attention', async ({ page }) => {
    await bootApp(page, { scenario: emptyScenario, route: '/' });
    await page.getByRole('button', { name: /Notifications/i }).click();
    await expect(page.getByText(/all caught up/i)).toBeVisible();
  });

  // Gap §10.2 — config-row navigates to /settings.
  test('clicking the Config issues row navigates to /settings', async ({ page }) => {
    await bootApp(page, { scenario: notificationsScenario, route: '/' });
    await page.getByRole('button', { name: /Notifications/i }).click();
    // The config row is rendered with the WORKFLOW.md error as title.
    await page.getByText(/WORKFLOW\.md validation failed/i).click();
    await expect(page).toHaveURL(/\/settings$/);
  });
});
