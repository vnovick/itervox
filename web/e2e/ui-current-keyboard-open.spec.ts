// CORE-068 — open issues from the Board and the List with the keyboard, and
// keep dnd-kit's keyboard drag working from the dedicated drag handle.
//
// Route-mocked lane: bootApp installs the API + SSE mocks. The scenario is
// built inline so ENG-1 sits in the first board column (Backlog) with an
// active column (In Progress) to its right.

import { expect, test, type Page } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import type { Scenario } from './fixtures/scenarios';
import { makeIssue } from '../src/test/fixtures/issues';
import { makeSnapshot } from '../src/test/fixtures/snapshots';

const scenario: Scenario = {
  snapshot: makeSnapshot({ backlogStates: ['Backlog'], activeStates: ['In Progress'] }),
  issues: [
    makeIssue({ identifier: 'ENG-1', title: 'Keyboard reachable card', state: 'Backlog' }),
    makeIssue({ identifier: 'ENG-2', title: 'Already in progress', state: 'In Progress' }),
  ],
  logs: {},
};

async function tabTo(page: Page, name: string) {
  const target = page.getByRole('button', { name, exact: true });
  await expect(target).toBeVisible();
  for (let i = 0; i < 80; i++) {
    await page.keyboard.press('Tab');
    if (await target.evaluate((el) => el === document.activeElement)) return;
  }
  throw new Error(`could not reach "${name}" with Tab`);
}

test.describe('CORE-068 keyboard open', () => {
  test('board card opens with Enter', async ({ page }) => {
    await bootApp(page, { scenario });
    await tabTo(page, 'Keyboard reachable card');
    await page.keyboard.press('Enter');
    await expect(page.getByRole('dialog', { name: 'ENG-1' })).toBeVisible();
    // Enter opened the issue; it did not pick the card up.
    await expect(
      page.locator('[data-testid="issue-card-drag-handle"][aria-pressed="true"]'),
    ).toHaveCount(0);
  });

  test('list row opens with Enter', async ({ page }) => {
    await bootApp(page, { scenario });
    await page.getByRole('button', { name: /^List/ }).click();
    await tabTo(page, 'Keyboard reachable card');
    await page.keyboard.press('Enter');
    await expect(page.getByRole('dialog', { name: 'ENG-1' })).toBeVisible();
  });

  test('board card keyboard drag still works', async ({ page }) => {
    const { api } = await bootApp(page, { scenario });
    await tabTo(page, 'Move ENG-1');
    // dnd-kit's live region doubles as the synchronisation point: each key
    // is pressed only after the previous step was announced.
    const live = page.locator('[aria-live]');
    await page.keyboard.press('Space');
    // The pick-up message is immediately followed by the first drag-over
    // message for the source column, so accept either.
    await expect(
      live.filter({ hasText: /Picked up ENG-1\.|ENG-1 is over column Backlog\./ }),
    ).toHaveCount(1);
    // B1 follow-up: while the card is lifted its source cell is a
    // placeholder; focus must stay on a "Move ENG-1" control, not fall to
    // <body>.
    const focusedLabel = () =>
      page.evaluate(() => document.activeElement?.getAttribute('aria-label') ?? null);
    await expect.poll(focusedLabel).toBe('Move ENG-1');
    // dnd-kit's KeyboardSensor attaches its keydown listener in a
    // setTimeout(0) after the pick-up, but React renders the announcement
    // synchronously, so the announcement alone does not prove the sensor is
    // listening. Under a loaded parallel run Chrome can dispatch the queued
    // ArrowRight before that timer task and the key is dropped (the M6-W1/W2
    // flake; reproduced deterministically by delaying zero-delay timers).
    // Waiting for one zero-delay timer of our own orders us after it.
    await page.evaluate(() => new Promise((resolve) => setTimeout(resolve, 0)));
    await page.keyboard.press('ArrowRight');
    await expect(live.filter({ hasText: 'ENG-1 is over column In Progress.' })).toHaveCount(1);
    await expect.poll(focusedLabel).toBe('Move ENG-1');
    await page.keyboard.press('Space');
    await expect
      .poll(
        () =>
          api.recordedMutations.find(
            (m) => m.method === 'PATCH' && m.url.endsWith('/api/v1/issues/ENG-1/state'),
          )?.body,
      )
      .toEqual({ state: 'In Progress' });
    // No detail dialog: the handle drags, it does not open.
    await expect(page.getByRole('dialog', { name: 'ENG-1' })).toHaveCount(0);
    // After the drop focus is back on the card's own handle.
    await expect(page.getByRole('button', { name: 'Move ENG-1' })).toBeFocused();
  });
});
