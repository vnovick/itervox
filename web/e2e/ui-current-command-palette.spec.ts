// CORE-095 — command palette and keyboard shortcuts, route-mocked.
import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { quickstartScenario } from './fixtures/scenarios';

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
