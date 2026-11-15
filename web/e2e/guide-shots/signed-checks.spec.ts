// Shots for site/src/content/docs/guides/signed-checks.mdx: a run held by a
// failing signed check, the reply that resumes it, and the issue panel's
// Signed checks section (failing then passing run; a record that does not
// verify).

import { expect, type Page } from '@playwright/test';
import { defineGuideShots } from './shoot';
import {
  heldMocks,
  heldScenario,
  ISSUE,
  passedMocks,
  passedScenario,
  tamperedMocks,
} from './fixtures/signed-checks';

const panel = (p: Page) => p.getByRole('dialog', { name: ISSUE });
const signed = (p: Page) => panel(p).getByTestId('signed-checks');
const inputPanel = (p: Page) =>
  panel(p).getByRole('heading', { name: 'Agent needs your input' }).locator('xpath=../..');

defineGuideShots('signed-checks', [
  {
    slug: '01-run-held',
    scenario: heldScenario,
    mocks: heldMocks,
    route: `/?issue=${ISSUE}`,
    ready: async (p) => {
      await expect(inputPanel(p).getByText(/exited with code 1/)).toBeVisible();
      await p
        .getByRole('textbox', { name: 'Reply to the agent' })
        .fill('Fix the bug in cart.go and leave CI alone; the test has to pass as written.');
    },
    crop: inputPanel,
  },
  {
    slug: '02-failing-run',
    scenario: heldScenario,
    mocks: heldMocks,
    route: `/?issue=${ISSUE}`,
    ready: (p) => expect(signed(p).getByTestId('signed-check')).toHaveCount(1),
    crop: signed,
  },
  {
    slug: '03-failing-then-passing',
    scenario: passedScenario,
    mocks: passedMocks,
    route: `/?issue=${ISSUE}`,
    ready: (p) => expect(signed(p).getByTestId('signed-checks-run')).toHaveCount(2),
    crop: signed,
  },
  {
    slug: '04-not-verified',
    scenario: passedScenario,
    mocks: tamperedMocks,
    route: `/?issue=${ISSUE}`,
    ready: (p) => expect(signed(p).getByText('attest: signature does not verify')).toBeVisible(),
    crop: signed,
  },
]);
