// T-64 — Issue mutations smoke (route-mocked).
//
// Asserts the dashboard mutation paths emit the expected POST request shape
// when the user clicks the cancel / terminate / refresh actions. The mock API
// records every mutation; the spec inspects `api.recordedMutations` to verify
// the URL, method, and payload that the optimistic-update layer sends.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { expectMutation } from './fixtures/assertions';
import { activeRunScenario, quickstartScenario } from './fixtures/scenarios';

test.describe('T-64 issue mutations smoke', () => {
  test('Pause button on running row → POST /api/v1/issues/<id>/cancel', async ({ page }) => {
    const { api } = await bootApp(page, { scenario: activeRunScenario });

    const target = activeRunScenario.snapshot.running[0];
    expect(target).toBeDefined();

    // Click the Pause button — text content is "⏸ Pause".
    await page.getByRole('button', { name: /Pause/ }).first().click();

    expectMutation(api.recordedMutations, {
      method: 'POST',
      urlMatcher: new RegExp(`/api/v1/issues/${target.identifier}/cancel$`),
    });
  });

  // CORE-021 added a confirm step to this button and CORE-073 renamed it
  // "Discard" (the spec's verb: /terminate moves the issue to the backlog).
  // The request is unchanged.
  test('Discard button on running row → confirm → POST /api/v1/issues/<id>/terminate', async ({
    page,
  }) => {
    const { api } = await bootApp(page, { scenario: activeRunScenario });

    const target = activeRunScenario.snapshot.running[0];

    await page
      .getByTestId(`running-row-${target.identifier}`)
      .getByRole('button', { name: /^✕ Discard$/ })
      .click();
    // First click only asks; nothing is sent yet.
    expect(
      api.recordedMutations.find((m) => m.url.endsWith(`/issues/${target.identifier}/terminate`)),
    ).toBeUndefined();
    await page.getByRole('button', { name: 'Yes, discard' }).click();

    expectMutation(api.recordedMutations, {
      method: 'POST',
      urlMatcher: new RegExp(`/api/v1/issues/${target.identifier}/terminate$`),
    });
  });

  test('Refresh issues button → POST /api/v1/refresh', async ({ page }) => {
    const { api } = await bootApp(page, { scenario: quickstartScenario });

    await page.getByRole('button', { name: 'Refresh issues' }).click();

    expectMutation(api.recordedMutations, {
      method: 'POST',
      urlMatcher: /\/api\/v1\/refresh$/,
    });
  });

  test('mutation paths emit Authorization: Bearer <token>', async ({ page }) => {
    const { api } = await bootApp(page, { scenario: quickstartScenario });

    const requestPromise = page.waitForRequest(
      (req) => req.url().endsWith('/api/v1/refresh') && req.method() === 'POST',
    );
    await page.getByRole('button', { name: 'Refresh issues' }).click();
    const request = await requestPromise;

    const auth = request.headers()['authorization'];
    expect(auth).toMatch(/^Bearer /);

    // Cross-check the mutation got recorded too.
    expect(api.recordedMutations.some((m) => m.url.endsWith('/api/v1/refresh'))).toBe(true);
  });
});
