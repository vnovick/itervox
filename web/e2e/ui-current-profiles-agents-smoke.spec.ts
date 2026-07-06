// T-66 — Profiles + Agents smoke (route-mocked).
//
// Boots /agents under settingsMatrixScenario which carries multiple profiles,
// reviewer config, and capacity. Asserts each section heading is visible, each
// profile name from the snapshot is rendered, and the page has zero console
// errors after hydration.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { settingsMatrixScenario } from './fixtures/scenarios';

test.describe('T-66 profiles + agents smoke', () => {
  test('/agents renders the three sections under settingsMatrixScenario', async ({ page }) => {
    const consoleErrors: string[] = [];
    page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(`console.error: ${msg.text()}`);
    });

    await bootApp(page, { scenario: settingsMatrixScenario, route: '/agents' });

    await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible();
    await expect(page.locator('h2#section-profiles')).toContainText('Profiles');
    await expect(page.locator('h2#section-reviewer')).toContainText('Code Review Agent');
    await expect(page.locator('h2#section-capacity')).toContainText('Capacity');

    expect(consoleErrors, `console errors:\n${consoleErrors.join('\n')}`).toEqual([]);
  });

  test('every profile name from the snapshot appears in the Profiles section', async ({ page }) => {
    await bootApp(page, { scenario: settingsMatrixScenario, route: '/agents' });

    const names = settingsMatrixScenario.snapshot.availableProfiles ?? [];
    expect(names.length).toBeGreaterThan(0);
    for (const name of names) {
      await expect(page.getByText(name).first()).toBeVisible();
    }
  });
});
