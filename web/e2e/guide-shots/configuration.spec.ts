// Shots for site/src/content/docs/configuration.mdx: the Settings page's
// Retries section (max retries, what happens when they run out).

import { expect } from '@playwright/test';
import { defineGuideShots } from './shoot';
import { settingsMatrixScenario } from '../fixtures/scenarios';
import { installTourData } from '../fixtures/tourData';

defineGuideShots('configuration', [
  {
    slug: '01-retries',
    scenario: settingsMatrixScenario,
    mocks: installTourData,
    route: '/settings',
    ready: (p) => expect(p.getByLabel('Max retries per issue')).toHaveValue('5'),
    crop: (p) => p.locator('section[aria-labelledby="section-retries"]'),
  },
]);
