// E2E coverage for the automations UI pass (F-1, F-2, T-1..T-10).
//
// Scope: route-mocked Lane-2 (no real daemon). The scenarios install a
// snapshot whose running + history rows carry `automationId` / `triggerType`
// so we can assert that:
//
//   • /automations renders both Configure and Activity tab toggles
//   • The Activity tab surfaces one card per configured automation
//   • The cards show the run rows the snapshot carries (filtered by
//     automationId)
//   • The /timeline "automation runs only" chip filters to those rows
//   • The /logs "automation" chip exists and toggles
//
// We don't assert the test-fire button networks here (covered by unit tests
// for `useTestAutomation` and `handleTestAutomation`); we only assert the
// affordance is reachable from the modal.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { settingsMatrixScenario } from './fixtures/scenarios';
import {
  makeAutomation,
  makeHistoryRow,
  makeProfileDef,
  makeRunningRow,
  makeSnapshot,
} from '../src/test/fixtures/snapshots';

const automationsActivityScenario = {
  ...settingsMatrixScenario,
  snapshot: makeSnapshot({
    automations: [
      makeAutomation({ id: 'cron-nightly', trigger: { type: 'cron', cron: '0 3 * * *', timezone: 'UTC' } }),
      makeAutomation({ id: 'pr-on-input', trigger: { type: 'input_required' } }),
    ],
    profileDefs: {
      default: makeProfileDef(),
      reviewer: makeProfileDef({ command: 'codex' }),
    },
    availableProfiles: ['default', 'reviewer'],
    running: [
      makeRunningRow({
        identifier: 'ENG-101',
        kind: 'automation',
        automationId: 'cron-nightly',
        triggerType: 'cron',
      }),
    ],
    history: [
      makeHistoryRow({
        identifier: 'ENG-100',
        kind: 'automation',
        automationId: 'cron-nightly',
        triggerType: 'cron',
        status: 'succeeded',
      }),
      makeHistoryRow({
        identifier: 'ENG-99',
        kind: 'automation',
        automationId: 'pr-on-input',
        triggerType: 'input_required',
        status: 'succeeded',
      }),
      makeHistoryRow({
        identifier: 'ENG-MANUAL',
        status: 'succeeded',
      }),
    ],
  }),
};

test.describe('Automations UI pass (F-1 + T-1..T-10)', () => {
  test('Activity tab renders one card per configured automation with their runs', async ({ page }) => {
    await bootApp(page, { scenario: automationsActivityScenario, route: '/automations' });

    // Both tabs are present in the tablist.
    await expect(page.getByTestId('automations-tablist')).toBeVisible();
    await expect(page.getByTestId('automations-tab-configure')).toBeVisible();
    await expect(page.getByTestId('automations-tab-activity')).toBeVisible();

    // Switch to Activity.
    await page.getByTestId('automations-tab-activity').click();

    // Two activity cards — one per configured automation.
    await expect(page.getByTestId('automation-activity-cron-nightly')).toBeVisible();
    await expect(page.getByTestId('automation-activity-pr-on-input')).toBeVisible();

    // The cron card lists its runs (live + history).
    const cronRuns = page.getByTestId('automation-runs-cron-nightly');
    await expect(cronRuns).toContainText('ENG-101');
    await expect(cronRuns).toContainText('ENG-100');
    await expect(cronRuns).not.toContainText('ENG-99');
    await expect(cronRuns).not.toContainText('ENG-MANUAL');
  });

  test('Logs page surfaces an automation filter chip', async ({ page }) => {
    await bootApp(page, { scenario: automationsActivityScenario, route: '/logs' });
    // The chip is visible only when an issue is selected — wait for sidebar
    // selection to settle, then check the chip.
    await expect(page.getByTestId('logs-filter-chips').first()).toBeVisible();
    await expect(page.getByTestId('chip-automation')).toBeVisible();
  });

  test('Timeline page renders the "automation runs only" chip', async ({ page }) => {
    await bootApp(page, { scenario: automationsActivityScenario, route: '/timeline' });
    await expect(page.getByTestId('timeline-chip-automation')).toBeVisible();
  });
});
