// Shots for site/src/content/docs/guides/guardrails.mdx: an approval command
// in the Needs-attention inbox and the issue's Now panel, then a held
// pipeline push on the phone (the hub's list and the issue's approval card).

import { expect, type Page } from '@playwright/test';
import { defineGuideShots } from './shoot';
import { DEPLOY_ISSUE, deployMocks, deployScenario, hubMocks } from './fixtures/guardrails';
import { heldScenario as laneHeldScenario } from './fixtures/lanes';

const PHONE = { width: 390, height: 844 };
const inbox = (p: Page) => p.getByTestId('attention-inbox');
// A narrower window keeps the inbox row readable when the docs scale it down.
const NARROW = { width: 1000, height: 900 };

defineGuideShots('guardrails', [
  {
    slug: '01-approval-in-inbox',
    scenario: deployScenario,
    mocks: deployMocks,
    ready: async (p) => {
      await p.getByRole('button', { name: `Reply to ${DEPLOY_ISSUE}` }).click();
      await p
        .getByRole('textbox', { name: `Reply to the agent on ${DEPLOY_ISSUE}` })
        .fill('no, deploy to staging first');
    },
    crop: inbox,
    viewport: NARROW,
  },
  {
    slug: '02-approval-in-issue',
    scenario: deployScenario,
    mocks: deployMocks,
    route: `/?issue=${DEPLOY_ISSUE}`,
    ready: (p) =>
      expect(p.getByTestId('issue-now-panel').getByText(/Waiting for approval/)).toBeVisible(),
    crop: (p) => p.getByTestId('issue-now-panel'),
  },
  {
    slug: '03-phone-hub-list',
    scenario: laneHeldScenario,
    mocks: hubMocks,
    route: '/hub/m',
    viewport: PHONE,
    ready: async (p) => {
      await expect(p.getByRole('heading', { name: 'Hub' })).toBeVisible();
      await expect(p.getByText(/changes \.github\/workflows\/ci\.yml/)).toBeVisible();
    },
  },
  {
    slug: '04-phone-approval-card',
    scenario: laneHeldScenario,
    mocks: hubMocks,
    route: '/hub/p/web-app/m/DEMO-1',
    viewport: PHONE,
    ready: async (p) => {
      await expect(p.getByTestId('mobile-decision')).toBeVisible();
      await expect(p.getByRole('button', { name: 'Approve' })).toBeVisible();
      await expect(p.getByTestId('pipeline-change-notice')).toBeVisible();
    },
  },
]);
