// T-61 stub smoke. Proves the route-mocked config + mockApi + bootApp wire
// together. Real coverage lives in T-62..T-71. Keep this stub minimal — its
// job is purely to lock in the harness.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { emptyScenario } from './fixtures/scenarios';

test.describe('T-61 harness stub', () => {
  test('boots the empty scenario without unhandled /api/v1/* requests', async ({ page }) => {
    const { api } = await bootApp(page, { scenario: emptyScenario });

    // The dashboard should render without throwing — the AuthGate sees the
    // ?token URL param, persists it, then issues authed requests against the
    // mocked /state which returns emptyScenario.snapshot.
    await expect(page.locator('body')).toBeVisible();

    // No spurious mutations on boot.
    expect(api.recordedMutations).toEqual([]);
  });
});
