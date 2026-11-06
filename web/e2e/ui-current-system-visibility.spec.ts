// System visibility (route-mocked): the dashboard warns when the workspace
// disk is low (and alerts when dispatch pauses for it); the issue view and
// the issue detail show the live run's resources (CPU, memory, processes,
// top child commands) and its Activity audit (commands, files written,
// connections; filter; the "sampled" badge; writes outside the workspace).
// The activity route is operator-scoped: a viewer gets a note, no request.

import { expect, test, type Page } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { SYSTEM_STATUS, installSystemVisibilityData } from './fixtures/tourData';
import { activeRunScenario, type Scenario } from './fixtures/scenarios';

const withSystem = (diskFreeGB: number, critical = false, callerScope?: string): Scenario => ({
  ...activeRunScenario,
  snapshot: {
    ...activeRunScenario.snapshot,
    projectName: 'web-app',
    capabilities: ['run_context', 'system_visibility'],
    system: SYSTEM_STATUS(diskFreeGB, critical),
    ...(callerScope ? { callerScope } : {}),
  },
});

async function openActivity(page: Page, scope = page.locator('body')) {
  await scope.getByRole('button', { name: /Activity/ }).click();
  await expect(scope.getByTestId('activity-mode')).toHaveText('sampled');
}

test.describe('system visibility', () => {
  test('the dashboard warns about low disk, and alerts while dispatch pauses for it', async ({
    page,
  }) => {
    await bootApp(page, { scenario: withSystem(3.2), route: '/' });
    const warn = page.getByTestId('system-health-disk');
    await expect(warn).toContainText('Low disk space: 3.2 GB free of 500 GB');
    await expect(warn).toHaveAttribute('role', 'status');

    await bootApp(page, { scenario: withSystem(1.5, true), route: '/' });
    const alert = page.getByTestId('system-health-disk');
    await expect(alert).toContainText('New runs are paused');
    await expect(alert).toHaveAttribute('role', 'alert');

    await bootApp(page, { scenario: withSystem(80), route: '/' });
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    await expect(page.getByTestId('system-health')).toHaveCount(0);
  });

  test('the issue view shows the run resources and its activity audit', async ({ page }) => {
    const activity: string[] = [];
    page.on('request', (r) => {
      if (r.url().includes('/api/v1/issues/') && r.url().includes('/activity'))
        activity.push(r.url());
    });
    await bootApp(page, {
      scenario: withSystem(80),
      route: '/focus/DEMO-RUN-1',
      beforeNavigate: installSystemVisibilityData,
    });
    const now = page.getByTestId('focus-now');
    const tiles = now.getByRole('list', { name: 'Resource use' });
    await expect(tiles).toContainText('83%');
    await expect(tiles).toContainText('700 MB');
    await expect(tiles).toContainText('peak 9');
    await expect(now.getByRole('table', { name: 'Top child commands' })).toContainText(
      'go test ./internal/greeting/...',
    );
    expect(activity, 'collapsed: no request yet').toHaveLength(0);

    await openActivity(page, now);
    await expect(now.getByTestId('activity-outside')).toHaveText('1 write outside the workspace');
    const commands = now.getByRole('table', { name: 'Commands', exact: true });
    await expect(commands.getByRole('row')).toHaveCount(5); // header + 4
    await now.getByRole('searchbox', { name: 'Filter activity' }).fill('git');
    await expect(commands.getByRole('row')).toHaveCount(2);
    await now.getByRole('button', { name: 'Clear activity filter' }).click();

    await now.getByRole('button', { name: /^Files/ }).click();
    const files = now.getByRole('table', { name: 'Files', exact: true });
    await expect(files).toContainText('/home/dev/.cache/go-build/trim.txt');
    await expect(files.getByText('outside')).toBeVisible();

    await now.getByRole('button', { name: /^Connections/ }).click();
    await expect(now.getByRole('table', { name: 'Connections', exact: true })).toContainText(
      '160.79.104.10:443',
    );
    await expect(now.getByTestId('activity-notes')).toContainText('may be missed');
    expect(activity.length).toBeGreaterThan(0);
    expect(activity[0]).toContain('/api/v1/issues/DEMO-RUN-1/activity');
  });

  test('the issue detail carries the same Now resources and Activity', async ({ page }) => {
    await bootApp(page, {
      scenario: withSystem(80),
      route: '/?issue=DEMO-RUN-1',
      beforeNavigate: installSystemVisibilityData,
    });
    const dialog = page.getByRole('dialog', { name: 'DEMO-RUN-1' });
    const panel = dialog.getByTestId('issue-now-panel');
    await expect(panel.getByRole('list', { name: 'Resource use' })).toContainText('700 MB');
    await openActivity(page, panel);
    await expect(panel.getByRole('table', { name: 'Commands', exact: true })).toContainText(
      'status --short',
    );
  });

  test('a viewer is told the audit needs an operator token and nothing is fetched', async ({
    page,
  }) => {
    const activity: string[] = [];
    page.on('request', (r) => {
      if (r.url().includes('/api/v1/issues/') && r.url().includes('/activity'))
        activity.push(r.url());
    });
    await bootApp(page, {
      scenario: withSystem(80, false, 'viewer'),
      route: '/focus/DEMO-RUN-1',
      beforeNavigate: installSystemVisibilityData,
    });
    const now = page.getByTestId('focus-now');
    await now.getByRole('button', { name: /Activity/ }).click();
    await expect(now.getByText(/needs an operator token/)).toBeVisible();
    expect(activity).toHaveLength(0);
  });
});
