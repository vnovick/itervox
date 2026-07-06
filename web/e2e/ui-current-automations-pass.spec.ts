// Lane 2 e2e coverage for the automations_ui_pass features (F-1..T-10).
//
// Each test exercises one Tier-2/Tier-3 surface against the
// `automationsPassScenario` fixture, asserting the integrated browser
// behaviour rather than the unit-level component behaviour already covered
// by Vitest. No daemon is required — every `/api/v1/*` is mocked.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { automationsPassScenario } from './fixtures/scenarios';

test.describe('automations_ui_pass — Lane 2 integration', () => {
  // T-1 — /automations exposes Configure + Activity tabs and persists choice.
  test('T-1: Configure ↔ Activity tabs are present and selectable', async ({ page }) => {
    await bootApp(page, { scenario: automationsPassScenario, route: '/automations' });
    await expect(page.getByRole('tablist', { name: /automations sections/i })).toBeVisible();
    const configure = page.getByRole('tab', { name: /configure/i });
    const activity = page.getByRole('tab', { name: /activity/i });
    await expect(configure).toHaveAttribute('aria-selected', 'true');
    await activity.click();
    await expect(activity).toHaveAttribute('aria-selected', 'true');
    await expect(configure).toHaveAttribute('aria-selected', 'false');
  });

  // T-2 — Activity tab renders one card per configured automation, each showing
  // matching history runs filtered by automationId.
  test('T-2: Activity tab renders per-automation cards with matching runs', async ({ page }) => {
    await bootApp(page, { scenario: automationsPassScenario, route: '/automations' });
    await page.getByRole('tab', { name: /activity/i }).click();

    // Cards have data-testid `automation-activity-<id>` (per AutomationActivityCard).
    await expect(page.locator('[data-testid="automation-activity-cron-nightly"]')).toBeVisible();
    await expect(page.locator('[data-testid="automation-activity-pr-on-input"]')).toBeVisible();

    // The cron-nightly card has 2 cron-fired runs in its run list, both today;
    // the manual DEMO-MANUAL row must NOT appear there.
    const cronCard = page.locator('[data-testid="automation-activity-cron-nightly"]');
    await expect(cronCard.getByText('DEMO-1')).toBeVisible();
    await expect(cronCard.getByText('DEMO-2')).toBeVisible();
    await expect(cronCard.getByText('DEMO-MANUAL')).toHaveCount(0);
  });

  // T-3 — Sparkline renders inside each activity card with non-zero data-max
  // when there are fires in the last 7 days.
  test('T-3: sparkline renders with the correct max for cron-nightly', async ({ page }) => {
    await bootApp(page, { scenario: automationsPassScenario, route: '/automations' });
    await page.getByRole('tab', { name: /activity/i }).click();
    const cronCard = page.locator('[data-testid="automation-activity-cron-nightly"]');
    const sparkline = cronCard.locator('[data-testid="sparkline"]');
    await expect(sparkline).toBeVisible();
    // Two fires today → max bucket count 2.
    await expect(sparkline).toHaveAttribute('data-max', '2');
  });

  // T-4 — Logs page automation chip filters to AUTOMATION FIRED entries.
  test('T-4: /logs automation chip filters to AUTOMATION FIRED lines', async ({ page }) => {
    await bootApp(page, { scenario: automationsPassScenario, route: '/logs' });
    // Sidebar lists DEMO-AUTO; click it to load its log buffer.
    await page.getByRole('button', { name: /DEMO-AUTO/ }).first().click();

    // Each Terminal entry is a `<span data-level=...>` — that's the production
    // selector. Vitest tests mock Terminal differently; the e2e harness uses
    // the real component, so match its actual DOM.
    const entries = page.locator('[data-level]');
    await expect(entries).toHaveCount(3);
    await page.locator('[data-testid="chip-automation"]').click();
    // Filtered view: only the AUTOMATION FIRED entry survives.
    await expect(entries).toHaveCount(1);
    await expect(entries.first()).toContainText('AUTOMATION FIRED');
    // Toggle off restores everything.
    await page.locator('[data-testid="chip-automation"]').click();
    await expect(entries).toHaveCount(3);
  });

  // T-5 — Timeline filter chip drops manual rows when toggled on.
  test('T-5: /timeline automation chip hides manual runs', async ({ page }) => {
    await bootApp(page, { scenario: automationsPassScenario, route: '/timeline' });
    const chip = page.locator('[data-testid="timeline-chip-automation"]');
    await expect(chip).toBeVisible();
    await expect(chip).toBeEnabled();

    // Pre-toggle: manual row visible in the sidebar.
    await expect(page.getByText('DEMO-MANUAL').first()).toBeVisible();
    await chip.click();
    await expect(chip).toHaveAttribute('aria-pressed', 'true');
    // Manual row hidden after toggle.
    await expect(page.getByText('DEMO-MANUAL')).toHaveCount(0);
    // Automation-driven rows still visible.
    await expect(page.getByText('DEMO-1').first()).toBeVisible();
  });

  // T-8 — Hero "automations today" tile shows the count and click-through
  // routes to /timeline with the chip enabled.
  test('T-8: hero automations-today tile counts today + navigates with chip set', async ({ page }) => {
    await bootApp(page, { scenario: automationsPassScenario, route: '/' });
    const tile = page.locator('[data-testid="hero-stat-automations-today"]');
    await expect(tile).toBeVisible();
    // Two automation-tagged history rows finished today (DEMO-OLD is yesterday;
    // DEMO-MANUAL has no automationId).
    await expect(tile).toContainText('2');
    await tile.click();
    await expect(page).toHaveURL(/\/timeline$/);
    await expect(page.locator('[data-testid="timeline-chip-automation"]')).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  });

  // T-10 — "Test fire" button POSTs to /api/v1/automations/{id}/test with the
  // identifier from the operator-supplied input, and the api mock records it.
  test('T-10: Test fire posts the identifier to /automations/:id/test', async ({ page }) => {
    const { api } = await bootApp(page, {
      scenario: automationsPassScenario,
      route: '/automations',
    });
    // Open the editor for cron-nightly via Edit button.
    const editButtons = page.getByRole('button', { name: /edit/i });
    await editButtons.first().click();

    const fireRegion = page.locator('[data-testid="automation-test-fire"]');
    await expect(fireRegion).toBeVisible();
    await fireRegion.locator('input[type="text"]').fill('DEMO-1');
    await fireRegion.getByRole('button', { name: /test fire/i }).click();

    // The mockApi handle records every non-GET request. Wait until the test-fire
    // POST shows up.
    await expect
      .poll(() => api.recordedMutations.find((m) => /\/automations\/.+\/test$/.test(m.url)) !== undefined)
      .toBe(true);
    const fired = api.recordedMutations.find((m) => /\/automations\/.+\/test$/.test(m.url));
    expect(fired).toBeDefined();
    expect(fired?.method).toBe('POST');
    expect(fired?.url).toMatch(/\/automations\/cron-nightly\/test$/);
    expect(fired?.body).toEqual({ identifier: 'DEMO-1' });
  });
});
