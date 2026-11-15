// Shots for site/src/content/docs/guides/lanes.mdx: a held pipeline push,
// from the Needs-attention row to the agent fixing the code instead.

import { expect, type Page } from '@playwright/test';
import { defineGuideShots } from './shoot';
import { fixedMocks, fixedScenario, heldMocks, heldScenario, ISSUE } from './fixtures/lanes';

const inbox = (p: Page) => p.getByTestId('attention-inbox');
// A narrower window keeps the inbox row readable when the docs scale it down.
const NARROW = { width: 1000, height: 900 };
const panel = (p: Page) => p.getByRole('dialog', { name: ISSUE });

async function openReply(p: Page, reply: string) {
  await p.getByRole('button', { name: `Reply to ${ISSUE}` }).click();
  await expect(inbox(p).getByTestId('pipeline-change-notice')).toBeVisible();
  await p.getByRole('textbox', { name: `Reply to the agent on ${ISSUE}` }).fill(reply);
}

defineGuideShots('lanes', [
  {
    slug: '01-needs-attention',
    scenario: heldScenario,
    mocks: heldMocks,
    ready: (p) => expect(inbox(p).getByText(/Pipeline change: the agent wants/)).toBeVisible(),
    crop: inbox,
    viewport: NARROW,
  },
  {
    slug: '02-issue-now-panel',
    scenario: heldScenario,
    mocks: heldMocks,
    route: `/?issue=${ISSUE}`,
    ready: async (p) => {
      await expect(panel(p).getByTestId('pipeline-change-notice')).toBeVisible();
      await expect(panel(p).getByText(/Waiting for approval/)).toBeVisible();
    },
    crop: (p) => p.getByTestId('issue-now-panel'),
  },
  {
    slug: '03-approve',
    scenario: heldScenario,
    mocks: heldMocks,
    ready: (p) => openReply(p, 'approve'),
    crop: inbox,
    viewport: NARROW,
  },
  {
    slug: '04-reject-with-reason',
    scenario: heldScenario,
    mocks: heldMocks,
    ready: (p) =>
      openReply(p, 'Rejected: fix the code, not the pipeline. The CI config stays as it is.'),
    crop: inbox,
    viewport: NARROW,
  },
  {
    slug: '05-agent-fixes-the-code',
    scenario: fixedScenario,
    mocks: fixedMocks,
    route: `/?issue=${ISSUE}`,
    ready: (p) => expect(panel(p).getByText(/fixing the total in cart.go/)).toBeVisible(),
    crop: (p) => p.getByTestId('issue-now-panel'),
  },
]);
