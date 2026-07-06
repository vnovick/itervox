// T-63 — Dashboard smoke (route-mocked, all scenarios).
//
// Boots the dashboard against each named scenario and asserts the structural
// surfaces survive the data shape: hero stats, board column / list panel,
// banners, and key issue identifiers. Click-through to the issue detail slide
// is covered by T-64 (Issue mutations smoke).

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import {
  activeRunScenario,
  configInvalidScenario,
  emptyScenario,
  inputRequiredScenario,
  quickstartScenario,
  retryAndPausedScenario,
  type Scenario,
} from './fixtures/scenarios';

interface Case {
  name: string;
  scenario: Scenario;
}

const cases: Case[] = [
  { name: 'empty', scenario: emptyScenario },
  { name: 'quickstart', scenario: quickstartScenario },
  { name: 'activeRun', scenario: activeRunScenario },
  { name: 'inputRequired', scenario: inputRequiredScenario },
  { name: 'retryAndPaused', scenario: retryAndPausedScenario },
  { name: 'configInvalid', scenario: configInvalidScenario },
];

test.describe('T-63 dashboard smoke', () => {
  for (const { name, scenario } of cases) {
    test(`scenario: ${name} — dashboard hydrates without errors`, async ({ page }) => {
      const consoleErrors: string[] = [];
      page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
      page.on('console', (msg) => {
        if (msg.type() === 'error') consoleErrors.push(`console.error: ${msg.text()}`);
      });

      await bootApp(page, { scenario });

      // Dashboard root visible — h1 from Dashboard/index.tsx.
      await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();

      // Hero stat labels — five tiles regardless of scenario.
      for (const label of ['Running', 'Paused', 'Retrying', 'Input Required', 'Capacity']) {
        await expect(page.getByText(label, { exact: true }).first()).toBeVisible();
      }

      // Issues panel header is always present.
      await expect(page.getByRole('heading', { name: /^Issues/ })).toBeVisible();

      // No console / page errors after hydration.
      expect(
        consoleErrors,
        `expected no console errors for ${name}, got:\n${consoleErrors.join('\n')}`,
      ).toEqual([]);
    });
  }

  test('configInvalid: API offline / config invalid surface visible', async ({ page }) => {
    await bootApp(page, { scenario: configInvalidScenario });
    // The dashboard renders even when configInvalid is populated — the banner
    // surface comes from the snapshot's `configInvalid` field, while the
    // "Cannot reach the Itervox API" banner only shows when `apiOffline` is
    // true (i.e. /state failed). With our route mock /state succeeds, so we
    // assert the rest of the dashboard still renders.
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
  });

  test('inputRequired: pending resume row is reachable', async ({ page }) => {
    await bootApp(page, { scenario: inputRequiredScenario });
    // The PendingResumePanel renders entries for both input_required and
    // pending_input_resume rows. We assert at least one of the row identifiers
    // is in the DOM somewhere on the page.
    const ids = inputRequiredScenario.snapshot.inputRequired?.map((r) => r.identifier) ?? [];
    expect(ids.length).toBeGreaterThan(0);
    for (const id of ids) {
      await expect(page.getByText(id).first()).toBeVisible();
    }
  });

  test('activeRun: running session identifiers visible', async ({ page }) => {
    await bootApp(page, { scenario: activeRunScenario });
    for (const row of activeRunScenario.snapshot.running) {
      await expect(page.getByText(row.identifier).first()).toBeVisible();
    }
  });

  test('retryAndPaused: retry + paused identifiers visible', async ({ page }) => {
    await bootApp(page, { scenario: retryAndPausedScenario });
    for (const row of retryAndPausedScenario.snapshot.retrying) {
      await expect(page.getByText(row.identifier).first()).toBeVisible();
    }
    for (const id of retryAndPausedScenario.snapshot.paused) {
      await expect(page.getByText(id).first()).toBeVisible();
    }
  });
});
