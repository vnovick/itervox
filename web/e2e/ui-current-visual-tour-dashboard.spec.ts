// Visual tour, dashboard overlays (route-mocked): the dashboard with every
// section populated, its issue views, and the overlays opened from it (issue
// detail of a running issue, automation queue detail, New task, inline and
// palette confirms) render in both themes without page errors. Screenshots
// go next to the main tour's (VISUAL_TOUR_DIR or test-results/visual-tour).

import { expect, test, type Page } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { activeRunScenario, type Scenario } from './fixtures/scenarios';
import { installRunContextData } from './fixtures/tourData';
import type { MockApiHandle } from './fixtures/mockApi';
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
// run_context and dispatch_pause: the daemon advertises both, so the issue
// detail shows its Now card, tabs, steering and playbook, and the status
// strip shows Pause dispatch.
const CAPABILITIES = ['run_context', 'dispatch_pause'];

const fullDashboard: Scenario = {
  snapshot: makeSnapshot({
    capabilities: CAPABILITIES,
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

// A running issue with everything the detail view can show: status changes,
// comments, a branch, priority and labels, over the live run context.
const runningDetail: Scenario = {
  ...activeRunScenario,
  snapshot: { ...activeRunScenario.snapshot, capabilities: CAPABILITIES },
  issues: activeRunScenario.issues.map((issue) =>
    issue.identifier === 'DEMO-RUN-1'
      ? {
          ...issue,
          title: 'Move the greeting into its own module',
          branchName: 'itervox/demo-run-1',
          priority: 2,
          labels: ['backend'],
          statusChanges: [
            { fromState: 'Todo', toState: 'In Progress', source: 'itervox', at: now },
            { fromState: 'Backlog', toState: 'Todo', source: 'tracker', at: now },
          ],
          comments: [
            {
              author: 'itervox',
              body: 'Should the old greeting stay as a fallback?',
              createdAt: now,
            },
            { author: 'vlad', body: 'Yes, keep it for one release.', createdAt: now },
          ],
        }
      : issue,
  ),
};

const OUT = process.env.VISUAL_TOUR_DIR ?? 'test-results/visual-tour';
const THEMES = ['light', 'dark'] as const;

interface Stop {
  name: string;
  route: string;
  scenario: Scenario;
  open?: (page: Page) => Promise<unknown>;
  /** Capture the viewport only (an overlay over a long page). Default: full page. */
  viewportOnly?: boolean;
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
    scenario: runningDetail,
    viewportOnly: true,
    open: async (p) => {
      const dialog = p.getByRole('dialog', { name: 'DEMO-RUN-1' });
      await expect(dialog).toBeVisible();
      await expect(dialog.getByRole('tab', { name: 'Changes' })).toBeVisible();
      await expect(dialog.getByText('Updating the greeting module')).toBeVisible();
    },
  },
  {
    name: 'queue-detail',
    viewportOnly: true,
    route: '/',
    scenario: fullDashboard,
    open: async (p) => {
      await p.getByRole('button', { name: 'Open DEMO-Q1 details' }).click();
      await expect(p.getByRole('dialog', { name: 'Automation qa-validation' })).toBeVisible();
    },
  },
  {
    name: 'new-task',
    viewportOnly: true,
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
    viewportOnly: true,
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
        await bootApp(page, {
          scenario: stop.scenario,
          route: stop.route,
          beforeNavigate: (api: MockApiHandle) => {
            installRunContextData(api);
          },
        });
        await hero(page);
        if (stop.open) await stop.open(page);
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
        await page.waitForTimeout(250);
        await page.screenshot({
          path: `${OUT}/${theme}/desktop-${stop.name}.png`,
          fullPage: !stop.viewportOnly,
        });
        expect(errors).toEqual([]);
      });
    }
  });
}
