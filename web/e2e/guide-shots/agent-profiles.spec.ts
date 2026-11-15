// Shots for site/src/content/docs/guides/agent-profiles.mdx: the profile
// editor's daemon actions (what a profile's agent may ask the daemon to do).

import { expect, type Page } from '@playwright/test';
import { defineGuideShots } from './shoot';
import { settingsMatrixScenario } from '../fixtures/scenarios';
import { installTourData } from '../fixtures/tourData';

const actions = (p: Page) => p.getByRole('group', { name: 'Daemon Actions' });

defineGuideShots('agent-profiles', [
  {
    slug: '01-daemon-actions',
    scenario: settingsMatrixScenario,
    mocks: installTourData,
    route: '/agents',
    ready: async (p) => {
      await expect(p.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible();
      await p.getByRole('button', { name: 'Edit' }).nth(1).click();
      await expect(actions(p)).toBeVisible();
    },
    crop: actions,
  },
]);
