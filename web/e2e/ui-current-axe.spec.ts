// M5-close — axe-core (WCAG 2.0/2.1/2.2 A + AA) over the operator surfaces
// in both themes, route-mocked, with a data set that fills every panel.
// Fails on any violation; there are no rule exclusions.

import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';
import { installMockApi } from './fixtures/mockApi';
import { installMockSse } from './fixtures/mockSse';
import { E2E_TOKEN, type Scenario } from './fixtures/scenarios';
import {
  makeHistoryRow,
  makeInputRequiredRow,
  makePendingInputResumeRow,
  makeRetryRow,
  makeRunningRow,
  makeSnapshot,
} from '../src/test/fixtures/snapshots';
import {
  makeBlockedIssue,
  makeInputRequiredIssue,
  makeIssue,
  makePausedIssue,
  makePendingResumeIssue,
  makeRetryingIssue,
  makeRunningIssue,
} from '../src/test/fixtures/issues';

const now = Date.now();
const iso = (ms: number) => new Date(ms).toISOString();

const richScenario: Scenario = {
  snapshot: makeSnapshot({
    maxConcurrentAgents: 4,
    completionState: 'In Review',
    running: [makeRunningRow({ identifier: 'DEMO-RUN-1', sessionId: 'sess-run-1' })],
    retrying: [makeRetryRow({ identifier: 'DEMO-3' })],
    paused: ['DEMO-PAUSED-1'],
    inputRequired: [makeInputRequiredRow(), makePendingInputResumeRow()],
    history: [makeHistoryRow({ identifier: 'DEMO-OLD', sessionId: 'sess-old' })],
    inlineInput: false,
    recentFailures: [
      {
        kind: 'worker_failed',
        identifier: 'GONE-9',
        message: 'exit 1',
        occurredAt: iso(now - 60_000),
        recordedAt: iso(now - 60_000),
        count: 1,
      },
      {
        kind: 'worker_failed',
        identifier: 'DEMO-3',
        message: 'exit 1',
        occurredAt: iso(now - 90_000),
        recordedAt: iso(now - 90_000),
        count: 2,
      },
    ],
    backendHealth: [
      { backend: 'claude', status: 'limited', limitedUntil: iso(now + 3_600_000), heldIssues: 2 },
    ],
    outboxEntries: [
      {
        id: 'ob-1',
        kind: 'comment',
        identifier: 'DEMO-1',
        attempts: 7,
        lastError: 'HTTP 500',
        degraded: true,
        enqueuedAt: iso(now - 600_000),
        nextAttemptAt: iso(now + 60_000),
      },
    ],
  }),
  issues: [
    makeIssue({ identifier: 'DEMO-1', title: 'Implement greeting', state: 'Todo' }),
    makeRunningIssue({ identifier: 'DEMO-RUN-1', title: 'Running work' }),
    makeRetryingIssue({ identifier: 'DEMO-3', title: 'Retry me' }),
    makePausedIssue({ identifier: 'DEMO-PAUSED-1', title: 'Paused work', state: 'In Progress' }),
    makeInputRequiredIssue(),
    makePendingResumeIssue(),
    makeBlockedIssue({ identifier: 'DEMO-BLOCKED-1' }),
  ],
  logs: {},
};

async function boot(page: Page, path: string, theme: 'light' | 'dark') {
  await page.addInitScript((t) => {
    localStorage.setItem('theme', t);
  }, theme);
  const api = await installMockApi(page, richScenario);
  await installMockSse(page, richScenario, 'one-shot');
  const sep = path.includes('?') ? '&' : '?';
  await page.goto(`${path}${sep}token=${encodeURIComponent(E2E_TOKEN)}`);
  return api;
}

function report(violations: Violation[]): string {
  return violations
    .map(
      (v) =>
        `${v.id} (${v.impact ?? '?'}) x${String(v.nodes.length)}\n` +
        v.nodes.map((n) => `    ${n.target} — ${n.summary}`).join('\n'),
    )
    .join('\n');
}

interface Violation {
  id: string;
  impact: string | null;
  nodes: { target: string; summary: string }[];
}

// CORE-088 — scans run through @axe-core/playwright's AxeBuilder (the
// acceptance names it); same WCAG 2.0/2.1/2.2 A + AA tag set as before.
async function axeViolations(page: Page): Promise<Violation[]> {
  const r = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze();
  return r.violations.map((v) => ({
    id: v.id,
    impact: v.impact ?? null,
    nodes: v.nodes.map((n) => ({
      target: n.target.join(' '),
      summary: (n.failureSummary ?? '').split('\n').slice(1, 2).join(' ').trim(),
    })),
  }));
}

type Ready = (page: Page) => Promise<void>;
const inboxVisible: Ready = async (p) => {
  await expect(p.getByRole('region', { name: /needs attention/i })).toBeVisible();
};

const settle: Ready = async (p) => {
  await expect(p.getByRole('main')).toBeVisible();
  await p.waitForTimeout(800);
};

const PAGES: [string, string, Ready][] = [
  ['dashboard board', '/', inboxVisible],
  ['logs', '/logs', settle],
  ['timeline', '/timeline', settle],
  ['dashboard list', '/?view=list', inboxVisible],
  [
    'issue slide',
    '/?issue=DEMO-INPUT-1',
    async (p) => {
      await expect(p.getByRole('dialog', { name: 'DEMO-INPUT-1' })).toBeVisible();
      // CORE-088 — the input-required reply panel is part of this scan.
      await expect(
        p.getByRole('dialog', { name: 'DEMO-INPUT-1' }).getByText('Agent needs your input'),
      ).toBeVisible();
    },
  ],
  [
    // CORE-088 — the comment composer (hidden while an issue is
    // input_required, so a different issue from the reply-panel scan above).
    'issue slide comment composer',
    '/?issue=DEMO-1',
    async (p) => {
      await expect(p.getByRole('dialog', { name: 'DEMO-1' })).toBeVisible();
      await expect(p.getByTestId('issue-comment-composer')).toBeVisible();
    },
  ],
  [
    'settings',
    '/settings',
    async (p) => {
      await expect(p.getByRole('button', { name: /Add host/ }).first()).toBeVisible();
      // CORE-088 — the Dependencies analysis-mode radio group is scanned too.
      await expect(p.getByRole('radiogroup').first()).toBeVisible();
    },
  ],
  [
    // CORE-095 — the command palette dialog (combobox + listbox).
    'command palette',
    '/',
    async (p) => {
      await expect(p.getByRole('region', { name: /needs attention/i })).toBeVisible();
      await p.getByRole('button', { name: /^Commands/ }).click();
      await expect(p.getByRole('dialog', { name: 'Command palette' })).toBeVisible();
    },
  ],
  [
    'add-host modal',
    '/settings',
    async (p) => {
      await p
        .getByRole('button', { name: /Add host/ })
        .first()
        .click();
      await expect(p.getByRole('dialog').last()).toBeVisible();
    },
  ],
];

for (const theme of ['dark', 'light'] as const) {
  for (const [name, path, ready] of PAGES) {
    test(`axe ${theme}: ${name} has no violations`, async ({ page }) => {
      await boot(page, path, theme);
      await ready(page);
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
      // Let entry transitions settle so contrast is measured on final colours.
      await page.waitForTimeout(400);
      const violations = await axeViolations(page);
      expect(violations, report(violations)).toEqual([]);
    });
  }

  test(`axe ${theme}: inbox reply box and error toast have no violations`, async ({ page }) => {
    const api = await boot(page, '/', theme);
    api.override(/provide-input$/, (route) =>
      route.fulfill({ status: 500, contentType: 'application/json', body: '{}' }),
    );
    const inbox = page.getByRole('region', { name: /needs attention/i });
    await inbox.getByRole('button', { name: 'Reply to DEMO-INPUT-1' }).click();
    await inbox.getByRole('textbox', { name: 'Reply to the agent on DEMO-INPUT-1' }).fill('x');
    await inbox.getByRole('button', { name: 'Send reply to DEMO-INPUT-1' }).click();
    await expect(page.getByRole('alert').getByTestId('toast')).toHaveCount(1);
    await page.waitForTimeout(400);
    const violations = await axeViolations(page);
    expect(violations, report(violations)).toEqual([]);
  });
}
