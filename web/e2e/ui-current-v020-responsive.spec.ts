import { expect, test, type Page } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import type { Scenario } from './fixtures/scenarios';
import { makeIssue } from '../src/test/fixtures/issues';
import {
  makeAutomation,
  makeHistoryRow,
  makeProfileDef,
  makeRunningRow,
  makeSnapshot,
  makeSSHHostInfo,
} from '../src/test/fixtures/snapshots';

const now = '2026-05-20T10:00:00.000Z';

const responsiveScenario: Scenario = {
  snapshot: makeSnapshot({
    maxConcurrentAgents: 3,
    running: [
      makeRunningRow({
        identifier: 'ENG-RUN',
        backend: 'codex',
        workerHost: 'ssh-a.example.com',
        automationId: 'cron-nightly',
        triggerType: 'cron',
      }),
    ],
    sshHosts: [makeSSHHostInfo({ host: 'ssh-a.example.com', description: 'Build worker' })],
    availableProfiles: ['default', 'pm'],
    profileDefs: {
      default: makeProfileDef({ command: 'claude' }),
      pm: makeProfileDef({
        command: 'codex --full-auto',
        backend: 'codex',
        allowedActions: ['comment', 'move_state', 'provide_input'],
      }),
    },
    automations: [
      makeAutomation({
        id: 'cron-nightly',
        profile: 'pm',
        trigger: { type: 'cron', cron: '*/5 * * * *', timezone: 'UTC' },
      }),
      makeAutomation({
        id: 'unblock-backlog-to-todo',
        profile: 'pm',
        trigger: { type: 'blockers_resolved' },
        filter: { states: ['Backlog'] },
        policy: { moveToState: 'Todo' },
      }),
    ],
    automationQueue: [
      {
        id: 'queue-cron',
        automationId: 'cron-nightly',
        triggerType: 'cron',
        identifier: 'ENG-Q1',
        title: 'Queued automation with long responsive title',
        issueState: 'Backlog',
        profile: 'pm',
        backend: 'codex',
        status: 'blocked',
        reason: 'no_slots',
        queuedAt: now,
        firedAt: now,
        attemptCount: 2,
        cron: '*/5 * * * *',
      },
      {
        id: 'queue-unblock',
        automationId: 'unblock-backlog-to-todo',
        triggerType: 'blockers_resolved',
        identifier: 'ENG-DEP',
        title: 'Dependency is now unblocked',
        issueState: 'Backlog',
        profile: 'pm',
        backend: 'codex',
        status: 'queued',
        reason: 'ready',
        queuedAt: now,
        firedAt: now,
        attemptCount: 1,
        moveToState: 'Todo',
      },
    ],
    automationQueueBackpressure: {
      length: 100,
      maxLength: 100,
      saturated: true,
      pausedProducers: true,
      rejectedSinceBoot: 3,
    },
    dependencyAudit: [
      {
        identifier: 'ENG-Q1',
        issueState: 'Backlog',
        status: 'blocked',
        sources: ['tracker_relation'],
        blockedBy: [{ identifier: 'ENG-BLOCKER', state: 'In Progress' }],
        unresolvedBlockers: [{ identifier: 'ENG-BLOCKER', state: 'In Progress' }],
        wasBlocked: true,
        lastAuditedAt: now,
      },
      {
        identifier: 'ENG-DEP',
        issueState: 'Backlog',
        status: 'unblocked',
        sources: ['tracker_relation'],
        resolvedBlockers: [{ identifier: 'ENG-BLOCKER', state: 'Done' }],
        wasBlocked: true,
        unblockedAt: now,
        lastTransitionVersion: 7,
        lastTransitionReason: 'all_blockers_terminal',
      },
    ],
    dependencyGraphNodes: [
      {
        id: 'ENG-BLOCKER',
        identifier: 'ENG-BLOCKER',
        title: 'Blocking issue',
        state: 'Done',
        status: 'unblocked',
        running: false,
        queued: false,
        terminal: true,
      },
      {
        id: 'ENG-Q1',
        identifier: 'ENG-Q1',
        title: 'Blocked issue',
        state: 'Backlog',
        status: 'blocked',
        running: false,
        queued: true,
        terminal: false,
      },
    ],
    dependencyGraphEdges: [
      {
        id: 'ENG-BLOCKER->ENG-Q1',
        sourceIdentifier: 'ENG-BLOCKER',
        targetIdentifier: 'ENG-Q1',
        sourceState: 'Done',
        targetState: 'Backlog',
        resolved: true,
        sourceKnown: true,
      },
    ],
    history: [
      makeHistoryRow({
        identifier: 'ENG-RUN',
        automationId: 'cron-nightly',
        triggerType: 'cron',
        finishedAt: now,
      }),
    ],
  }),
  issues: [
    makeIssue({
      identifier: 'ENG-Q1',
      title: 'Queued automation with long responsive title',
      state: 'Backlog',
      statusChanges: [
        {
          fromState: 'Todo',
          toState: 'In Progress',
          source: 'worker_lifecycle',
          profileName: 'pm',
          backend: 'codex',
          workerHost: 'ssh-a.example.com',
          at: now,
        },
        {
          fromState: 'In Progress',
          toState: 'Backlog',
          source: 'automation',
          automationId: 'cron-nightly',
          triggerType: 'cron',
          at: now,
        },
      ],
    }),
    makeIssue({ identifier: 'ENG-BLOCKER', title: 'Blocking issue', state: 'Done' }),
    makeIssue({ identifier: 'ENG-DEP', title: 'Dependency is now unblocked', state: 'Backlog' }),
  ],
  logs: {},
};

async function expectNoPageOverflow(page: Page) {
  const sizes = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(sizes.scrollWidth).toBeLessThanOrEqual(sizes.clientWidth + 2);
}

test.describe('v0.2.0 responsive dashboard surfaces', () => {
  test('captures desktop, tablet, and mobile dashboard screenshots', async ({ page }) => {
    for (const viewport of [
      { name: 'desktop', width: 1440, height: 900 },
      { name: 'tablet', width: 768, height: 900 },
      { name: 'mobile', width: 390, height: 844 },
    ]) {
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await bootApp(page, { scenario: responsiveScenario, route: '/' });

      await expect(page.getByText(/Automation queue full 100\/100/)).toBeVisible();
      await expect(page.getByRole('heading', { name: /Automation Queue/i })).toBeVisible();
      await expectNoPageOverflow(page);
      await page.screenshot({
        path: `test-results/v020-responsive-${viewport.name}-dashboard.png`,
        fullPage: true,
      });
    }
  });

  test('captures mobile issue, queue detail, and Deps graph panels', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await bootApp(page, { scenario: responsiveScenario, route: '/' });

    await page.getByRole('button', { name: /open issue ENG-Q1/i }).click();
    await expect(page.getByRole('heading', { name: /Status changes/i })).toBeVisible();
    await expectNoPageOverflow(page);
    await page.screenshot({
      path: 'test-results/v020-responsive-mobile-issue-detail.png',
      fullPage: true,
    });

    await page.getByRole('button', { name: /close panel/i }).click();
    await page.getByRole('button', { name: /open ENG-Q1 details/i }).click();
    await expect(page.getByRole('dialog', { name: /Automation cron-nightly/i })).toBeVisible();
    await expectNoPageOverflow(page);
    await page.screenshot({
      path: 'test-results/v020-responsive-mobile-queue-detail.png',
      fullPage: true,
    });

    await page.getByRole('button', { name: /close panel/i }).click();
    await page.getByRole('button', { name: /^Deps$/i }).click();
    await expect(page.getByTestId('rf__node-ENG-BLOCKER')).toBeVisible();
    await page.getByTestId('rf__node-ENG-Q1').click();
    await expect(page.getByRole('heading', { name: /Status changes/i })).toBeVisible();
    await expectNoPageOverflow(page);
    await page.screenshot({
      path: 'test-results/v020-responsive-mobile-deps-issue.png',
      fullPage: true,
    });
  });
});
