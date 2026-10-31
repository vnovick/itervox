// Visual tour, dashboard overlays (route-mocked): the dashboard with every
// section populated, its issue views, and the overlays opened from it (issue
// detail of a running issue, automation queue detail, New task, inline and
// palette confirms) render in both themes without page errors. Screenshots
// go next to the main tour's (VISUAL_TOUR_DIR or test-results/visual-tour).

import { expect, test, type Page } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { activeRunScenario, type Scenario } from './fixtures/scenarios';
import {
  makeInputRequiredIssue,
  makeIssue,
  makePausedIssue,
  makeRunningIssue,
} from '../src/test/fixtures/issues';
import {
  makeAutomation,
  makeInputRequiredRow,
  makeProfileDef,
  makeRunningRow,
  makeSSHHostInfo,
  makeSnapshot,
} from '../src/test/fixtures/snapshots';

const now = new Date().toISOString();

// Every dashboard section has rows: needs input, running + paused sessions,
// a queued automation with an open blocker, two worker hosts.
const fullDashboard: Scenario = {
  snapshot: makeSnapshot({
    maxConcurrentAgents: 3,
    running: [
      makeRunningRow({ identifier: 'DEMO-RUN-1', backend: 'claude', lastEvent: 'tool_use:Edit' }),
      makeRunningRow({
        identifier: 'DEMO-RUN-2',
        backend: 'codex',
        workerHost: 'ssh-a.example.com',
        kind: 'reviewer',
        lastEvent: 'tool_use:Read',
      }),
    ],
    paused: ['DEMO-PAUSED-1'],
    inputRequired: [makeInputRequiredRow()],
    sshHosts: [makeSSHHostInfo({ host: 'ssh-a.example.com', description: 'Build worker' })],
    availableProfiles: ['default', 'pm'],
    profileDefs: {
      default: makeProfileDef({ command: 'claude' }),
      pm: makeProfileDef({ command: 'codex', backend: 'codex', allowedActions: ['comment'] }),
    },
    automations: [
      makeAutomation({
        id: 'qa-validation',
        profile: 'pm',
        trigger: { type: 'pr_opened' },
      }),
    ],
    automationQueue: [
      {
        id: 'queue-qa',
        automationId: 'qa-validation',
        triggerType: 'pr_opened',
        identifier: 'DEMO-Q1',
        title: 'Queued automation run',
        issueState: 'Todo',
        profile: 'pm',
        backend: 'codex',
        status: 'queued',
        reason: 'no_slots',
        queuedAt: now,
        firedAt: now,
        attemptCount: 1,
      },
    ],
    dependencyAudit: [
      {
        identifier: 'DEMO-Q1',
        issueState: 'Todo',
        status: 'blocked',
        sources: ['tracker_relation'],
        blockedBy: [{ identifier: 'DEMO-RUN-1', state: 'In Progress' }],
        unresolvedBlockers: [{ identifier: 'DEMO-RUN-1', state: 'In Progress' }],
        wasBlocked: true,
        lastAuditedAt: now,
      },
    ],
  }),
  issues: [
    makeRunningIssue({ identifier: 'DEMO-RUN-1' }),
    makeRunningIssue({ identifier: 'DEMO-RUN-2', agentBackend: 'codex' }),
    makePausedIssue({ identifier: 'DEMO-PAUSED-1' }),
    makeInputRequiredIssue(),
    makeIssue({ identifier: 'DEMO-Q1', title: 'Queued automation run', state: 'Todo' }),
  ],
  logs: {},
};

const OUT = process.env.VISUAL_TOUR_DIR ?? 'test-results/visual-tour';
const THEMES = ['light', 'dark'] as const;

interface Stop {
  name: string;
  route: string;
  scenario: Scenario;
  open?: (page: Page) => Promise<unknown>;
}

const hero = (page: Page) =>
  expect(page.getByRole('heading', { name: 'Autonomous agentic harness' })).toBeVisible();

const stops: Stop[] = [
  { name: 'dashboard-full', route: '/', scenario: fullDashboard },
  ...(['list', 'agents', 'deps', 'notifications'] as const).map((view) => ({
    name: `dashboard-${view}`,
    route: `/?view=${view}`,
    scenario: fullDashboard,
  })),
  {
    name: 'issue-detail-running',
    route: '/?issue=DEMO-RUN-1',
    scenario: activeRunScenario,
    open: (p) => expect(p.getByRole('dialog', { name: 'DEMO-RUN-1' })).toBeVisible(),
  },
  {
    name: 'queue-detail',
    route: '/',
    scenario: fullDashboard,
    open: async (p) => {
      await p.getByRole('button', { name: 'Open DEMO-Q1 details' }).click();
      await expect(p.getByRole('dialog', { name: 'Automation qa-validation' })).toBeVisible();
    },
  },
  {
    name: 'new-task',
    route: '/?newTask=1',
    scenario: activeRunScenario,
    open: (p) => expect(p.getByRole('dialog', { name: 'New task' })).toBeVisible(),
  },
  {
    name: 'discard-inline-confirm',
    route: '/',
    scenario: activeRunScenario,
    open: async (p) => {
      await p.getByRole('button', { name: '✕ Discard' }).first().click();
      await expect(p.getByRole('button', { name: 'Yes, discard' })).toBeVisible();
    },
  },
  {
    name: 'command-palette-confirm',
    route: '/',
    scenario: activeRunScenario,
    open: async (p) => {
      await p.keyboard.press('ControlOrMeta+k');
      const box = p.getByRole('combobox', { name: 'Search commands and issues' });
      await box.fill('Discard DEMO-RUN-1');
      await box.press('Enter');
      await expect(p.getByRole('button', { name: 'Yes, discard DEMO-RUN-1' })).toBeVisible();
    },
  },
];

for (const theme of THEMES) {
  test.describe(`visual tour, dashboard overlays (${theme})`, () => {
    test.beforeEach(async ({ page }) => {
      await page.addInitScript((t) => window.localStorage.setItem('theme', t), theme);
    });

    for (const stop of stops) {
      test(`${stop.name} renders`, async ({ page }) => {
        const errors: string[] = [];
        page.on('pageerror', (e) => errors.push(e.message));
        await bootApp(page, { scenario: stop.scenario, route: stop.route });
        await hero(page);
        if (stop.open) await stop.open(page);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        await page.waitForTimeout(250);
        await page.screenshot({ path: `${OUT}/${theme}/desktop-${stop.name}.png`, fullPage: true });
        expect(errors).toEqual([]);
      });
    }
  });
}
