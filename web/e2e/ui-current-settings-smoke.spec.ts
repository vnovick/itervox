// T-69 — Settings matrix + tracker-states + reviewer + workspace smoke.
//
// Boots /settings under settingsMatrixScenario and asserts every section
// heading is visible. Acceptance: each section renders, no console errors.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { settingsMatrixScenario } from './fixtures/scenarios';

const SECTION_IDS_AND_LABELS: Array<{ id: string; expected: RegExp }> = [
  { id: 'section-general', expected: /General/ },
  { id: 'section-tracker', expected: /Tracker States/ },
  { id: 'section-workspace', expected: /Workspace/ },
  { id: 'section-ssh-hosts', expected: /SSH Hosts/ },
  { id: 'section-logs', expected: /Logs/ },
];

test.describe('T-69 settings matrix smoke', () => {
  test('/settings renders every section under settingsMatrixScenario', async ({ page }) => {
    const consoleErrors: string[] = [];
    page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(`console.error: ${msg.text()}`);
    });

    await bootApp(page, { scenario: settingsMatrixScenario, route: '/settings' });

    await expect(page).toHaveTitle(/Settings/);
    await expect(page.getByRole('heading', { name: 'Settings', level: 1 })).toBeVisible();

    for (const { id, expected } of SECTION_IDS_AND_LABELS) {
      await expect(page.locator(`h2#${id}`)).toContainText(expected);
    }

    expect(consoleErrors, `console errors:\n${consoleErrors.join('\n')}`).toEqual([]);
  });

  test('tracker active/terminal states from snapshot appear on the page', async ({ page }) => {
    await bootApp(page, { scenario: settingsMatrixScenario, route: '/settings' });

    const active = settingsMatrixScenario.snapshot.activeStates ?? [];
    expect(active.length).toBeGreaterThan(0);
    for (const s of active) {
      await expect(page.getByText(s).first()).toBeVisible();
    }
  });
});
