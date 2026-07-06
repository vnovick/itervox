// T-65 — Timeline + Logs smoke (route-mocked).
//
// Boots the Timeline route and the Logs route under the timelineLogsScenario
// (every history status + every log event variant) and asserts each route
// renders without throwing, page title sets correctly, and the key
// identifiers are reachable in the DOM.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { timelineLogsScenario } from './fixtures/scenarios';

test.describe('T-65 timeline + logs smoke', () => {
  test('timeline route hydrates from the timelineLogsScenario history', async ({ page }) => {
    const consoleErrors: string[] = [];
    page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(`console.error: ${msg.text()}`);
    });

    await bootApp(page, { scenario: timelineLogsScenario, route: '/timeline' });

    // PageMeta sets <title> via react-helmet-async.
    await expect(page).toHaveTitle(/Timeline/);

    // Every history identifier from the scenario should be reachable somewhere
    // on the timeline page (sidebar issue list or detail panel).
    const historyIds = (timelineLogsScenario.snapshot.history ?? []).map((h) => h.identifier);
    expect(historyIds.length).toBeGreaterThan(0);
    for (const id of historyIds) {
      await expect(page.getByText(id).first()).toBeVisible();
    }

    expect(consoleErrors, `console errors:\n${consoleErrors.join('\n')}`).toEqual([]);
  });

  test('logs route hydrates from the timelineLogsScenario issue logs', async ({ page }) => {
    const consoleErrors: string[] = [];
    page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(`console.error: ${msg.text()}`);
    });

    await bootApp(page, { scenario: timelineLogsScenario, route: '/logs' });

    await expect(page).toHaveTitle(/Logs/);

    expect(consoleErrors, `console errors:\n${consoleErrors.join('\n')}`).toEqual([]);
  });

  test('timeline route survives the empty scenario without console errors', async ({ page }) => {
    const consoleErrors: string[] = [];
    page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(`console.error: ${msg.text()}`);
    });

    const empty = { ...timelineLogsScenario, snapshot: { ...timelineLogsScenario.snapshot, history: [], running: [] }, issues: [] };
    await bootApp(page, { scenario: empty, route: '/timeline' });

    await expect(page).toHaveTitle(/Timeline/);
    expect(consoleErrors, `console errors:\n${consoleErrors.join('\n')}`).toEqual([]);
  });
});
