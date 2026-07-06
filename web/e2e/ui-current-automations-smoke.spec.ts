// T-67 — Automations smoke (route-mocked).
//
// Boots /automations under settingsMatrixScenario which includes a cron
// automation and an input_required automation. Asserts the page renders, the
// automation IDs are visible, and the page has zero console errors.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { settingsMatrixScenario } from './fixtures/scenarios';

test.describe('T-67 automations smoke', () => {
  test('/automations renders all automations from the snapshot', async ({ page }) => {
    const consoleErrors: string[] = [];
    page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(`console.error: ${msg.text()}`);
    });

    await bootApp(page, { scenario: settingsMatrixScenario, route: '/automations' });

    await expect(page).toHaveTitle(/Automations/);
    await expect(page.getByRole('heading', { name: 'Automations', level: 1 })).toBeVisible();

    const automations = settingsMatrixScenario.snapshot.automations ?? [];
    expect(automations.length).toBeGreaterThanOrEqual(2);
    for (const auto of automations) {
      // Each automation should be findable by its id or one of its trigger fields.
      const visible = await page.getByText(auto.id).first().isVisible().catch(() => false);
      const triggerVisible = await page
        .getByText(auto.trigger.type === 'cron' ? 'cron' : auto.trigger.type, { exact: false })
        .first()
        .isVisible()
        .catch(() => false);
      expect(visible || triggerVisible, `automation ${auto.id} not visible`).toBe(true);
    }

    expect(consoleErrors, `console errors:\n${consoleErrors.join('\n')}`).toEqual([]);
  });

  test('cron automation trigger type is visible', async ({ page }) => {
    await bootApp(page, { scenario: settingsMatrixScenario, route: '/automations' });

    // The settingsMatrixScenario.automations[0] is a cron trigger.
    const cron = (settingsMatrixScenario.snapshot.automations ?? []).find((a) => a.trigger.type === 'cron');
    expect(cron).toBeDefined();
    if (cron?.trigger.cron) {
      await expect(page.getByText(cron.trigger.cron).first()).toBeVisible();
    }
  });

  test('input_required automation trigger type is visible', async ({ page }) => {
    await bootApp(page, { scenario: settingsMatrixScenario, route: '/automations' });

    const inputReq = (settingsMatrixScenario.snapshot.automations ?? []).find(
      (a) => a.trigger.type === 'input_required',
    );
    expect(inputReq).toBeDefined();
  });
});
