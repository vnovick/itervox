// M5-B3 — attention inbox (CORE-077), URL-addressable issue selection
// (CORE-079) and the attention title/badge (CORE-078), route-mocked.

import { expect, test, type Page } from '@playwright/test';
import { installMockApi, type MockApiHandle } from './fixtures/mockApi';
import { installMockSse } from './fixtures/mockSse';
import {
  E2E_TOKEN,
  inputRequiredScenario,
  quickstartScenario,
  type Scenario,
} from './fixtures/scenarios';

/** bootApp appends ?token= to the route, which breaks routes with a query. */
async function bootAt(page: Page, scenario: Scenario, path: string): Promise<MockApiHandle> {
  const api = await installMockApi(page, scenario);
  await installMockSse(page, scenario, 'one-shot');
  const sep = path.includes('?') ? '&' : '?';
  await page.goto(`${path}${sep}token=${encodeURIComponent(E2E_TOKEN)}`);
  return api;
}

const replyScenario: Scenario = {
  ...inputRequiredScenario,
  snapshot: { ...inputRequiredScenario.snapshot, inlineInput: false },
};

test.describe('CORE-079 URL-addressable issue selection', () => {
  test('deep-link opens issue slide', async ({ page }) => {
    await bootAt(page, quickstartScenario, '/?issue=DEMO-1');
    await expect(page.getByRole('dialog', { name: 'DEMO-1' })).toBeVisible();
    // The consumed token never comes back; the issue param survives reload.
    await expect(page).toHaveURL(/\/\?issue=DEMO-1$/);
    await page.reload();
    await expect(page.getByRole('dialog', { name: 'DEMO-1' })).toBeVisible();
  });

  test('Back closes the slide and restores the previous URL', async ({ page }) => {
    await bootAt(page, quickstartScenario, '/?view=list');
    await expect(page).toHaveURL(/\/\?view=list$/);
    await page.getByRole('button', { name: 'Implement greeting' }).first().click();
    await expect(page.getByRole('dialog', { name: 'DEMO-1' })).toBeVisible();
    await expect(page).toHaveURL(/[?&]issue=DEMO-1/);
    await expect(page).toHaveURL(/[?&]view=list/);
    await page.goBack();
    await expect(page.getByRole('dialog', { name: 'DEMO-1' })).toBeHidden();
    await expect(page).toHaveURL(/\/\?view=list$/);
    await page.goForward();
    await expect(page.getByRole('dialog', { name: 'DEMO-1' })).toBeVisible();
  });

  test('initial load with ?issue= before the issues response resolves still opens the slide', async ({
    page,
  }) => {
    const api = await installMockApi(page, quickstartScenario);
    await installMockSse(page, quickstartScenario, 'one-shot');
    let listRequests = 0;
    api.override(/\/api\/v1\/issues$/, async (route) => {
      listRequests++;
      await new Promise((r) => setTimeout(r, 1500));
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(quickstartScenario.issues),
      });
    });
    await page.goto(`/?issue=DEMO-2&token=${encodeURIComponent(E2E_TOKEN)}`);
    await expect(page.getByRole('dialog', { name: 'DEMO-2' })).toBeVisible();
    expect(listRequests).toBeGreaterThan(0);
    // Still selected after the slow list settles (it contains DEMO-2).
    await page.waitForTimeout(1700);
    await expect(page).toHaveURL(/[?&]issue=DEMO-2/);
  });

  test('an unknown ?issue= degrades gracefully and is removed', async ({ page }) => {
    await bootAt(page, quickstartScenario, '/?issue=NOPE-404');
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    await expect(page).not.toHaveURL(/issue=/);
    await expect(page.getByRole('dialog', { name: 'NOPE-404' })).toHaveCount(0);
  });
});

test.describe('CORE-077 attention inbox', () => {
  test('lists input-required work at the top and Reply posts provide-input', async ({ page }) => {
    const api = await bootAt(page, replyScenario, '/');
    const inbox = page.getByRole('region', { name: /needs attention/i });
    await expect(inbox).toBeVisible();
    // Pinned above the live-ops strip.
    const inboxBox = await inbox.boundingBox();
    const stripBox = await page.getByTestId('live-ops-strip').boundingBox();
    expect(inboxBox && stripBox && inboxBox.y < stripBox.y).toBe(true);
    // Resuming rows are read-only.
    const resuming = page.getByTestId('attention-item-resuming-DEMO-INPUT-2');
    await expect(resuming).toContainText(/reply pending/i);
    await expect(resuming.getByRole('button', { name: /Reply/ })).toHaveCount(0);

    await inbox.getByRole('button', { name: 'Reply to DEMO-INPUT-1' }).click();
    await inbox
      .getByRole('textbox', { name: 'Reply to the agent on DEMO-INPUT-1' })
      .fill('Use the main branch');
    await inbox.getByRole('button', { name: 'Send reply to DEMO-INPUT-1' }).click();
    await expect
      .poll(() =>
        api.recordedMutations.find((m) =>
          m.url.endsWith('/api/v1/issues/DEMO-INPUT-1/provide-input'),
        ),
      )
      .toMatchObject({ method: 'POST', body: { message: 'Use the main branch' } });
  });

  test('attention count reaches the nav badge and the page title', async ({ page }) => {
    await bootAt(page, replyScenario, '/');
    const count = await page.getByTestId('attention-inbox-count').textContent();
    expect(Number(count)).toBeGreaterThan(0);
    await expect(page).toHaveTitle(new RegExp(`^\\(${String(count)}\\) Itervox \\| Dashboard$`));
    await expect(
      page.getByRole('link', { name: `Dashboard, ${String(count)} need attention` }).first(),
    ).toBeVisible();
  });
});
