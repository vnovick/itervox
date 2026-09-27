// CORE-075 — the global /api/v1/logs SSE stream is owned by NarrativeFeed
// (Dashboard only). Other routes must not hold that connection open.

import { expect, test, type Page } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { quickstartScenario } from './fixtures/scenarios';

function countGlobalLogStreams(page: Page) {
  const seen = { count: 0 };
  page.on('request', (req) => {
    if (new URL(req.url()).pathname === '/api/v1/logs') seen.count += 1;
  });
  return seen;
}

test.describe('CORE-075 global log stream scope', () => {
  test('is not opened on a page without NarrativeFeed, and opens on the Dashboard', async ({
    page,
  }) => {
    const seen = countGlobalLogStreams(page);
    await bootApp(page, { scenario: quickstartScenario, route: '/settings' });
    await expect(page.getByRole('link', { name: 'Dashboard' })).toBeVisible();
    // Give any app-root stream time to connect before asserting its absence.
    await page.waitForTimeout(1_000);
    expect(seen.count).toBe(0);

    await page.getByRole('link', { name: 'Dashboard' }).click();
    await expect(page.getByTestId('narrative-feed')).toBeVisible();
    await expect.poll(() => seen.count).toBeGreaterThan(0);
  });
});
