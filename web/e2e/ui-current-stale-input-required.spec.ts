// Gap A — visual smoke for the stale-input-required badge on IssueCard.
//
// Boots the dashboard with two input-required entries:
//   • a fresh one (queued 5m ago)  — the IssueCard should NOT render a
//     "⚠ Stale" badge.
//   • a stale one  (queued 3h ago, snapshot row marks `stale: true`) — the
//     card should render the badge with an age-formatted tooltip.
//
// The spec also captures a screenshot under test-results/ for human
// review; the baseline is intentionally not snapshotted so trivial layout
// changes don't fail the spec.

import { expect, test } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { makeIssue } from '../src/test/fixtures/issues';
import { makeInputRequiredRow, makeSnapshot } from '../src/test/fixtures/snapshots';
import { formatRFC3339, minutesAgo } from '../src/test/fixtures/time';

const issuesAndSnapshot = (() => {
  const stale = makeInputRequiredRow({
    identifier: 'ENG-STALE',
    state: 'input_required',
    context: 'PM agent asked for clarification on acceptance criteria.',
    queuedAt: formatRFC3339(minutesAgo(180)), // 3h ago
    stale: true,
    ageMinutes: 180,
  });
  const fresh = makeInputRequiredRow({
    identifier: 'ENG-FRESH',
    state: 'input_required',
    context: 'Quick clarification needed.',
    queuedAt: formatRFC3339(minutesAgo(5)),
    stale: false,
    ageMinutes: 5,
  });
  const snapshot = makeSnapshot({
    inputRequired: [stale, fresh],
    activeStates: ['In Progress'],
    backlogStates: ['Backlog'],
    completionState: 'Done',
  });
  const issues = [
    makeIssue({
      identifier: 'ENG-STALE',
      title: 'Stale issue (queued 3h ago)',
      state: 'In Progress',
      orchestratorState: 'input_required',
    }),
    makeIssue({
      identifier: 'ENG-FRESH',
      title: 'Fresh issue (queued 5m ago)',
      state: 'In Progress',
      orchestratorState: 'input_required',
    }),
  ];
  return { snapshot, issues };
})();

const scenario = {
  snapshot: issuesAndSnapshot.snapshot,
  issues: issuesAndSnapshot.issues,
  logs: {} as Record<string, never>,
};

test.describe('Stale input-required badge (gap A)', () => {
  test('renders only on issues whose snapshot row carries stale:true', async ({ page }) => {
    await bootApp(page, { scenario, route: '/' });

    // Wait until the board has both cards rendered.
    await expect(page.getByText('Stale issue (queued 3h ago)')).toBeVisible();
    await expect(page.getByText('Fresh issue (queued 5m ago)')).toBeVisible();

    // Stale badge appears exactly once — for ENG-STALE.
    const staleBadges = page.getByTestId('issue-card-stale-badge');
    await expect(staleBadges).toHaveCount(1);
    await expect(staleBadges.first()).toContainText('Stale');
    await expect(staleBadges.first()).toHaveAttribute(
      'title',
      /Input requested .* ago — likely abandoned/,
    );

    // Capture a screenshot so the operator can eyeball the styling.
    await page.screenshot({
      path: 'test-results/stale-input-required-badge.png',
      fullPage: false,
    });
  });
});
