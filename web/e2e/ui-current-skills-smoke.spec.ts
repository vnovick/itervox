// T-97 — Phase-1 skills inventory e2e smoke (route-mocked).
//
// Boots the dashboard with a minimal skills inventory served via mockApi
// overrides and asserts the SkillsCard renders, Re-scan triggers the right
// mutation, and the Fix button on a non-destructive recommendation calls
// /api/v1/skills/fix.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { quickstartScenario } from './fixtures/scenarios';

const fakeInventory = {
  ScanTime: '2026-04-29T00:00:00Z',
  Skills: [
    {
      Name: 'demo-skill',
      Description: 'a demo',
      Provider: 'claude',
      Source: 'project',
      FilePath: '/p/.claude/skills/demo/SKILL.md',
      ApproxTokens: 50,
      TriggerPatterns: null,
    },
  ],
  MCPServers: [],
  Hooks: [],
  Instructions: [],
  Plugins: [],
  Issues: [],
};

const fakeIssues = [
  {
    ID: 'UNUSED_PROFILE',
    Severity: 'info',
    Title: 'Profile not seen in recent runs',
    Description: 'Profile orphan is configured but unused.',
    Affected: ['orphan'],
    Fix: {
      Label: 'Disable profile',
      Action: 'edit-yaml',
      Target: 'agent.profiles.orphan.enabled',
      Destructive: false,
    },
  },
];

test.describe('T-97 skills smoke', () => {
  test('SkillsCard hydrates from /api/v1/skills/inventory + /issues', async ({ page }) => {
    const { api } = await bootApp(page, { scenario: quickstartScenario, route: '/settings' });
    api.override(/\/api\/v1\/skills\/inventory$/, async (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(fakeInventory),
      }),
    );
    api.override(/\/api\/v1\/skills\/issues$/, async (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(fakeIssues),
      }),
    );

    // Reload to pick up the new overrides.
    await page.reload();

    await expect(page.locator('h2#section-skills')).toContainText('Skills Inventory');
    await expect(page.getByText('Profile not seen in recent runs')).toBeVisible();
  });

  test('Re-scan button POSTs to /api/v1/skills/scan', async ({ page }) => {
    const { api } = await bootApp(page, { scenario: quickstartScenario, route: '/settings' });
    api.override(/\/api\/v1\/skills\/inventory$/, async (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(fakeInventory),
      }),
    );
    await page.reload();

    await page.getByRole('button', { name: 'Re-scan' }).click();

    expect(
      api.recordedMutations.some(
        (m) => m.method === 'POST' && m.url.endsWith('/api/v1/skills/scan'),
      ),
      'expected POST /api/v1/skills/scan to be recorded',
    ).toBe(true);
  });

  test('Analytics section renders runtime recommendations (T-104/T-106)', async ({ page }) => {
    const { api } = await bootApp(page, { scenario: quickstartScenario, route: '/settings' });
    api.override(/\/api\/v1\/skills\/inventory$/, async (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(fakeInventory),
      }),
    );
    api.override(/\/api\/v1\/skills\/analytics$/, async (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          GeneratedAt: '2026-04-29T00:00:00Z',
          SkillStats: [
            {
              CapabilityID: 'demo-skill',
              ApproxTokens: 50,
              RuntimeLoads: 10,
              Configured: true,
              RuntimeVerified: true,
              LastSeenAt: '2026-04-29T00:00:00Z',
            },
          ],
          HookStats: [],
          ProfileCosts: [],
          Recommendations: null,
        }),
      }),
    );
    api.override(/\/api\/v1\/skills\/analytics\/recommendations$/, async (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          {
            ID: 'CONFIGURED_NOT_LOADED',
            Severity: 'info',
            Category: 'staleness',
            Title: 'Configured capability never observed at runtime',
            Description: 'demo-skill is configured but absent from runtime evidence.',
            Affected: ['demo-skill'],
          },
        ]),
      }),
    );
    await page.reload();

    await expect(page.getByText(/Runtime analytics/)).toBeVisible();
    await expect(page.getByText('Configured capability never observed at runtime')).toBeVisible();
  });

  test('Non-destructive Fix button POSTs to /api/v1/skills/fix without confirm', async ({ page }) => {
    const { api } = await bootApp(page, { scenario: quickstartScenario, route: '/settings' });
    api.override(/\/api\/v1\/skills\/inventory$/, async (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(fakeInventory),
      }),
    );
    api.override(/\/api\/v1\/skills\/issues$/, async (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(fakeIssues),
      }),
    );
    await page.reload();

    await page.getByRole('button', { name: 'Disable profile' }).click();

    // The Fix is non-destructive (Destructive=false), so no confirm dialog.
    expect(
      api.recordedMutations.some(
        (m) => m.method === 'POST' && m.url.endsWith('/api/v1/skills/fix'),
      ),
      'expected POST /api/v1/skills/fix to be recorded',
    ).toBe(true);
  });
});
