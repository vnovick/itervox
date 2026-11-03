// CORE-095 — command palette and keyboard shortcuts, route-mocked.
import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import type { MockApiHandle } from './fixtures/mockApi';
import { quickstartScenario, settingsMatrixScenario, type Scenario } from './fixtures/scenarios';
import { installTourData } from './fixtures/tourData';

// The palette's richer sources: profiles and automations (settings matrix
// snapshot) plus the issues of the quickstart, rules / memory / settings
// (tour data), one session, and the dispatch-pause capability.
const richScenario: Scenario = {
  ...settingsMatrixScenario,
  issues: quickstartScenario.issues,
  snapshot: {
    ...settingsMatrixScenario.snapshot,
    capabilities: [...(settingsMatrixScenario.snapshot.capabilities ?? []), 'dispatch_pause'],
  },
};
const richData = (api: MockApiHandle) => {
  installTourData(api);
  api.override(/\/api\/v1\/sessions$/, (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        generatedAt: new Date().toISOString(),
        sessions: [
          {
            id: 'sess-demo-2',
            owner: 'itervox',
            identifier: 'DEMO-2',
            status: 'input_required',
          },
        ],
      }),
    }),
  );
};

test.describe('CORE-095 command palette', () => {
  test('command palette opens with Mod+K and jumps to issue', async ({ page }) => {
    await bootApp(page, { scenario: quickstartScenario });
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    await page.keyboard.press('ControlOrMeta+k');
    const palette = page.getByRole('dialog', { name: 'Command palette' });
    await expect(palette).toBeVisible();
    const box = palette.getByRole('combobox', { name: /search commands/i });
    await expect(box).toBeFocused();
    await box.fill('DEMO-2');
    await page.keyboard.press('Enter');
    await expect(palette).toBeHidden();
    await expect(page.getByRole('dialog', { name: 'DEMO-2' })).toBeVisible();
    await expect(page).toHaveURL(/[?&]issue=DEMO-2/);
  });

  test('g-sequences navigate; shortcuts are ignored while typing', async ({ page }) => {
    await bootApp(page, { scenario: quickstartScenario });
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
    await page.keyboard.press('g');
    await page.keyboard.press('l');
    await expect(page).toHaveURL(/\/logs/);
    // Typing "g t" into a search field types text, it does not navigate.
    const search = page.getByRole('searchbox', { name: 'Search log issues' });
    await search.click();
    await page.keyboard.type('gt');
    await expect(search).toHaveValue('gt');
    await expect(page).toHaveURL(/\/logs/);
    // Escape closes the palette and focus returns to the opener button.
    const opener = page.getByRole('button', { name: /^Commands/ });
    await opener.click();
    await expect(page.getByRole('dialog', { name: 'Command palette' })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog', { name: 'Command palette' })).toBeHidden();
    await expect(opener).toBeFocused();
  });
});

test.describe('command palette: sources, prefixes, ranking, recents, actions', () => {
  test.beforeEach(async ({ page }) => {
    await bootApp(page, { scenario: richScenario, beforeNavigate: richData });
    await expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();
  });

  const openPalette = async (page: import('@playwright/test').Page) => {
    await page.keyboard.press('ControlOrMeta+k');
    const box = page.getByRole('combobox', { name: /search commands/i });
    await expect(box).toBeFocused();
    return box;
  };
  const options = (page: import('@playwright/test').Page) =>
    page.getByRole('listbox', { name: 'Commands' }).getByRole('option');

  test('prefixes: ? lists them, / pages, @ profiles, # issues, > actions', async ({ page }) => {
    const box = await openPalette(page);
    await box.fill('?');
    await expect(options(page)).toHaveCount(5);
    await options(page).filter({ hasText: 'Pages' }).click();
    await expect(box).toHaveValue('/');
    await expect(options(page).first()).toContainText('Go to Dashboard');
    await expect(options(page).filter({ hasText: 'Open DEMO-1' })).toHaveCount(0);
    await box.fill('@rev');
    await expect(options(page).first()).toContainText('Open reviewer in Agents');
    await expect(options(page).filter({ hasNotText: '@' })).toHaveCount(0);
    await box.fill('#demo');
    await expect(options(page).first()).toContainText('Open DEMO-');
    await expect(options(page).filter({ hasText: 'Go to' })).toHaveCount(0);
    await box.fill('>');
    await expect(options(page).first()).toContainText('New task');
    await expect(options(page).filter({ hasText: 'Go to' })).toHaveCount(0);
  });

  test('fuzzy: "dmo2" ranks DEMO-2 first and Enter opens it', async ({ page }) => {
    const box = await openPalette(page);
    await box.fill('dmo2');
    await expect(options(page).first()).toContainText('Open DEMO-2');
    await page.keyboard.press('Enter');
    await expect(page.getByRole('dialog', { name: 'DEMO-2' })).toBeVisible();
  });

  test('recents: a command run once is listed first, also after a reload', async ({ page }) => {
    let box = await openPalette(page);
    await expect(options(page).first()).toContainText('New task');
    await box.fill('go to timeline');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/timeline/);
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Timeline' }).first()).toBeVisible();
    box = await openPalette(page);
    await expect(page.getByRole('dialog', { name: 'Command palette' })).toContainText('Recent');
    await expect(options(page).first()).toContainText('Go to Timeline');
    await expect(options(page).first()).toContainText('g t');
  });

  test('an action: Pause dispatch asks first, then posts the pause', async ({ page }) => {
    const box = await openPalette(page);
    const posted = page.waitForRequest(
      (r) => r.method() === 'POST' && r.url().includes('/api/v1/dispatch/pause'),
    );
    await box.fill('>pause dispatch');
    await page.keyboard.press('Enter');
    const confirm = page
      .getByRole('dialog', { name: 'Command palette' })
      .getByRole('button', { name: 'Pause dispatch' });
    await expect(confirm).toBeFocused();
    await confirm.click();
    await posted;
    await expect(page.getByRole('dialog', { name: 'Command palette' })).toBeHidden();
  });

  test('memory, rules and sessions are searchable', async ({ page }) => {
    const box = await openPalette(page);
    await box.fill('staging fridays');
    await expect(options(page).first()).toContainText('Staging is read-only on Fridays.');
    await box.fill('no-token-logs');
    await expect(options(page).first()).toContainText('Never log the API token.');
    await box.fill('input required');
    await expect(options(page).filter({ hasText: 'DEMO-2' }).first()).toBeVisible();
  });

  test('a settings field: the All settings section opens it and focuses the field', async ({
    page,
  }) => {
    const box = await openPalette(page);
    await box.fill('max concurrent agents');
    await expect(options(page).first()).toContainText('agent.max_concurrent_agents');
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/settings\?setting=agent\.max_concurrent_agents/);
    const field = page.getByTestId('setting-agent.max_concurrent_agents').locator('input');
    await expect(field).toBeFocused();
    await expect(field).toBeInViewport();
  });
});
