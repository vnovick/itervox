// Visual tour (route-mocked): every page and overlay renders in both themes
// without page errors. Screenshots go to test-results/visual-tour/<theme>/
// (or VISUAL_TOUR_DIR) so a reviewer can compare them with the design.

import { expect, test, type Page } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { installMockApi } from './fixtures/mockApi';
import { installMockSse } from './fixtures/mockSse';
import type { MockApiHandle } from './fixtures/mockApi';
import { installTourData } from './fixtures/tourData';
import {
  activeRunScenario,
  inputRequiredScenario,
  mobileShellScenario,
  quickstartScenario,
  settingsMatrixScenario,
  timelineLogsScenario,
  type Scenario,
} from './fixtures/scenarios';

// The pause-dispatch control only renders when the daemon advertises it.
const pausable: Scenario = {
  ...activeRunScenario,
  snapshot: {
    ...activeRunScenario.snapshot,
    capabilities: [...(activeRunScenario.snapshot.capabilities ?? []), 'dispatch_pause'],
  },
};

const OUT = process.env.VISUAL_TOUR_DIR ?? 'test-results/visual-tour';
const THEMES = ['light', 'dark'] as const;

interface Stop {
  name: string;
  route: string;
  scenario: Scenario;
  ready: (page: Page) => Promise<unknown>;
  open?: (page: Page) => Promise<unknown>;
  /** Route overrides installed before navigation. */
  setup?: (api: MockApiHandle) => void;
}

const heading = (name: string | RegExp) => (page: Page) =>
  expect(page.getByRole('heading', { name }).first()).toBeVisible();

const pages: Stop[] = [
  {
    name: 'dashboard',
    route: '/',
    scenario: activeRunScenario,
    ready: heading('Autonomous agentic harness'),
  },
  {
    name: 'timeline',
    route: '/timeline',
    scenario: timelineLogsScenario,
    ready: (p) => p.waitForLoadState('networkidle'),
  },
  {
    name: 'sessions',
    route: '/sessions',
    scenario: activeRunScenario,
    ready: (p) => p.waitForLoadState('networkidle'),
  },
  {
    name: 'logs',
    route: '/logs',
    scenario: timelineLogsScenario,
    ready: (p) => p.waitForLoadState('networkidle'),
  },
  { name: 'agents', route: '/agents', scenario: settingsMatrixScenario, ready: heading('Agents') },
  {
    name: 'automations',
    route: '/automations',
    scenario: settingsMatrixScenario,
    ready: heading('Automations'),
  },
  {
    name: 'rules',
    route: '/rules',
    scenario: settingsMatrixScenario,
    setup: installTourData,
    ready: (p) => expect(p.getByTestId('rules-preview')).toBeVisible(),
  },
  {
    name: 'repo',
    route: '/repo',
    scenario: settingsMatrixScenario,
    setup: installTourData,
    ready: (p) => expect(p.getByText('Tracked files current')).toBeVisible(),
  },
  {
    name: 'memory',
    route: '/memory',
    scenario: settingsMatrixScenario,
    setup: installTourData,
    ready: (p) => expect(p.getByTestId('memory-list')).toBeVisible(),
  },
  {
    name: 'settings',
    route: '/settings',
    scenario: settingsMatrixScenario,
    setup: installTourData,
    ready: heading('Settings'),
  },
  {
    name: 'not-found',
    route: '/no-such-page',
    scenario: settingsMatrixScenario,
    ready: (p) => expect(p.getByRole('link', { name: 'Back to Home Page' })).toBeVisible(),
  },
];

const overlays: Stop[] = [
  {
    name: 'issue-detail',
    route: '/?issue=DEMO-1',
    scenario: quickstartScenario,
    ready: (p) => expect(p.getByRole('dialog', { name: 'DEMO-1' })).toBeVisible(),
  },
  {
    name: 'input-required',
    route: '/',
    scenario: inputRequiredScenario,
    ready: heading('Autonomous agentic harness'),
  },
  {
    name: 'command-palette',
    route: '/',
    scenario: activeRunScenario,
    ready: heading('Autonomous agentic harness'),
    open: async (p) => {
      await p.keyboard.press('ControlOrMeta+k');
      await expect(p.getByRole('combobox', { name: 'Search commands and issues' })).toBeVisible();
    },
  },
  {
    name: 'pause-dispatch-confirm',
    route: '/',
    scenario: pausable,
    ready: heading('Autonomous agentic harness'),
    open: async (p) => {
      await p.getByRole('button', { name: 'Pause dispatch' }).first().click();
      await expect(p.getByRole('dialog', { name: /Pause all dispatch/ })).toBeVisible();
    },
  },
  {
    name: 'profile-form',
    route: '/agents',
    scenario: settingsMatrixScenario,
    ready: heading('Agents'),
    open: async (p) => {
      await p.getByRole('button', { name: 'Add Agent Profile' }).first().click();
      await expect(p.getByRole('dialog').first()).toBeVisible();
    },
  },
  {
    name: 'memory-lessons',
    route: '/memory',
    scenario: settingsMatrixScenario,
    setup: installTourData,
    ready: (p) => expect(p.getByTestId('memory-list')).toBeVisible(),
    open: async (p) => {
      await p.getByRole('tab', { name: /Lessons/ }).click();
      await expect(p.getByTestId('lessons-list')).toBeVisible();
    },
  },
  {
    name: 'add-worker-host',
    route: '/settings',
    scenario: settingsMatrixScenario,
    ready: heading('Settings'),
    open: async (p) => {
      await p
        .getByRole('button', { name: /Add (worker )?host/i })
        .first()
        .click();
      await expect(p.getByRole('dialog').first()).toBeVisible();
    },
  },
  {
    name: 'automation-form',
    route: '/automations',
    scenario: settingsMatrixScenario,
    ready: heading('Automations'),
    open: async (p) => {
      await p.getByRole('button', { name: 'Add Automation' }).first().click();
      await expect(p.getByRole('dialog').first()).toBeVisible();
    },
  },
];

const viewportOf = (project: string) => (project.includes('mobile') ? 'mobile' : 'desktop');

async function shoot(page: Page, theme: string, name: string, viewport: string) {
  await page.waitForTimeout(250);
  await page.screenshot({
    path: `${OUT}/${theme}/${viewport}-${name}.png`,
    fullPage: true,
    timeout: 30_000,
  });
}

for (const theme of THEMES) {
  test.describe(`visual tour (${theme})`, () => {
    // Full-page captures of the long pages (Settings with every schema
    // field) take a while under backdrop-filter.
    test.describe.configure({ timeout: 60_000 });
    test.beforeEach(async ({ page }) => {
      await page.addInitScript((t) => window.localStorage.setItem('theme', t), theme);
    });

    for (const stop of [...pages, ...overlays]) {
      test(`${stop.name} renders`, async ({ page }, info) => {
        const errors: string[] = [];
        page.on('pageerror', (e) => errors.push(e.message));
        await bootApp(page, { scenario: stop.scenario, route: stop.route, setup: stop.setup });
        await stop.ready(page);
        if (stop.open) await stop.open(page);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        await shoot(
          page,
          theme,
          stop.name,
          info.project.name.includes('mobile') ? 'mobile' : 'desktop',
        );
        expect(errors).toEqual([]);
      });
    }

    test('sign-in screen renders', async ({ page }, info) => {
      const api = await installMockApi(page, quickstartScenario);
      await installMockSse(page, quickstartScenario);
      api.override(/\/api\/v1\/state$/, (route) =>
        route.fulfill({ status: 401, contentType: 'application/json', body: '{}' }),
      );
      await page.goto('/');
      await expect(page.getByText(/Sign in to Itervox/)).toBeVisible();
      await shoot(
        page,
        theme,
        'sign-in',
        info.project.name.includes('mobile') ? 'mobile' : 'desktop',
      );
    });

    test('mobile issue page renders', async ({ page }, info) => {
      await bootApp(page, {
        scenario: mobileShellScenario,
        route: '/m/DEMO-1',
        setup: installTourData,
      });
      await page.waitForLoadState('networkidle');
      await shoot(
        page,
        theme,
        'mobile-issue',
        info.project.name.includes('mobile') ? 'mobile' : 'desktop',
      );
    });

    test('server-down screen renders', async ({ page }, info) => {
      const api = await installMockApi(page, quickstartScenario);
      await installMockSse(page, quickstartScenario);
      api.override(/\/api\/v1\/health$/, (route) => route.abort('connectionrefused'));
      await page.goto('/');
      await expect(page.getByRole('heading', { name: "Can't reach the daemon" })).toBeVisible();
      await shoot(page, theme, 'server-down', viewportOf(info.project.name));
    });

    test('pair device screen renders', async ({ page }, info) => {
      await installMockApi(page, quickstartScenario);
      await installMockSse(page, quickstartScenario);
      await page.goto('/hub/pair#code=demo-pairing-code');
      await expect(
        page.getByRole('heading', { name: 'Pair this device with the hub' }),
      ).toBeVisible();
      await shoot(page, theme, 'pair-device', viewportOf(info.project.name));
    });

    test('hub page renders', async ({ page }, info) => {
      await bootApp(page, {
        scenario: inputRequiredScenario,
        route: '/hub/',
        setup: installTourData,
      });
      await expect(
        page.getByRole('list', { name: 'Projects' }).getByText('docs-site', { exact: true }),
      ).toBeVisible();
      await shoot(page, theme, 'hub', viewportOf(info.project.name));
    });

    test('navigation drawer renders', async ({ page }, info) => {
      test.skip(!info.project.name.includes('mobile'), 'the drawer is the phone-width navigation');
      await bootApp(page, { scenario: activeRunScenario, route: '/' });
      await page.getByTestId('mobile-menu-button').click();
      const drawer = page.getByRole('dialog', { name: 'Navigation' });
      await expect(drawer.getByRole('link', { name: 'Settings' })).toBeVisible();
      await page.waitForTimeout(300);
      await page.screenshot({ path: `${OUT}/${theme}/mobile-drawer.png` });
    });

    test('hub notifications view renders', async ({ page }, info) => {
      await bootApp(page, {
        scenario: inputRequiredScenario,
        route: '/hub/?view=notifications',
        setup: installTourData,
      });
      await expect(page.getByRole('table', { name: 'web-app notification routes' })).toBeVisible();
      await shoot(page, theme, 'hub-notifications', viewportOf(info.project.name));
    });

    test('hub phone page renders', async ({ page }, info) => {
      await bootApp(page, {
        scenario: inputRequiredScenario,
        route: '/hub/m',
        setup: installTourData,
      });
      await expect(page.getByRole('heading', { name: 'Hub' })).toBeVisible();
      await page.waitForLoadState('networkidle');
      await shoot(page, theme, 'hub-phone', viewportOf(info.project.name));
    });
  });
}
