// Review and continue (route-mocked): the Changes tab collects a draft review
// (a range comment, a line comment and an overall note) and "Send to agent"
// posts it as ONE request. With no live run the send bar offers "Move to
// <first active state>" and "Also post on the PR", both on by default; each
// checkbox reaches the request body. With a live run "Move to" is hidden.

import { expect, test, type Page, type Route } from '@playwright/test';
import { bootApp } from './fixtures/commands';
import { installChangesData } from './fixtures/tourData';
import { activeRunScenario, type Scenario } from './fixtures/scenarios';
import { makeIssue } from '../src/test/fixtures/issues';

const reviewScenario: Scenario = {
  ...activeRunScenario,
  snapshot: {
    ...activeRunScenario.snapshot,
    activeStates: ['In Progress'],
    capabilities: ['run_context', 'change_review', 'review_continue'],
  },
  issues: [
    ...activeRunScenario.issues,
    makeIssue({ identifier: 'DEMO-REV-1', title: 'Trim the greeting', state: 'In Review' }),
  ],
};

const FILE = /^M ?internal\/greeting\/greeting\.go/;

async function openChanges(page: Page, identifier: string, sent: unknown[]) {
  await bootApp(page, {
    scenario: reviewScenario,
    route: `/?issue=${identifier}`,
    beforeNavigate: (api) => {
      // Registered first, so it answers before installChangesData's mock.
      api.override(/\/api\/v1\/issues\/[^/]+\/review-continue$/, async (route: Route) => {
        sent.push(route.request().postDataJSON());
        await route.fulfill({
          status: 202,
          contentType: 'application/json',
          body: JSON.stringify({
            agent: { delivery: 'queued', queuedAt: '2026-10-31T12:00:00Z', movedTo: 'In Progress' },
            pr: { posted: true, comments: 2 },
          }),
        });
      });
      installChangesData(api);
    },
  });
  const dialog = page.getByRole('dialog', { name: identifier });
  await dialog.getByRole('tab', { name: 'Changes' }).click();
  await dialog.getByRole('button', { name: FILE }).click();
  await expect(dialog.getByTestId('diff-viewer')).toBeVisible();
  return dialog;
}

async function draftReview(dialog: ReturnType<Page['getByRole']>) {
  await dialog.getByRole('button', { name: 'Select line 10', exact: true }).click();
  await dialog
    .getByRole('button', { name: 'Select line 12', exact: true })
    .click({ modifiers: ['Shift'] });
  await dialog
    .getByRole('textbox', { name: 'Comment' })
    .fill('Name the fallback constant in the doc comment.');
  await dialog.getByRole('button', { name: 'Add to review' }).click();
  await dialog.getByRole('button', { name: 'Select line 14', exact: true }).click();
  await dialog
    .getByRole('textbox', { name: 'Comment' })
    .fill('Drop this comment: the code says it.');
  await dialog.getByRole('button', { name: 'Add to review' }).click();
  await dialog
    .getByRole('textbox', { name: 'Overall note' })
    .fill('Close; also cover blank names in the test.');
  await expect(dialog.getByTestId('review-draft-count')).toHaveText('2 comments');
}

test.describe('review and continue', () => {
  test('sends the draft with "Move to" and "Also post on the PR" on by default', async ({
    page,
  }) => {
    const sent: unknown[] = [];
    const dialog = await openChanges(page, 'DEMO-REV-1', sent);
    await draftReview(dialog);
    await expect(dialog.getByRole('checkbox', { name: 'Move to In Progress' })).toBeChecked();
    await expect(dialog.getByRole('checkbox', { name: 'Also post on the PR' })).toBeChecked();
    await dialog.getByRole('button', { name: 'Send to agent' }).click();
    await expect.poll(() => sent.length).toBe(1);
    expect(sent[0]).toMatchObject({
      moveToActive: true,
      alsoPostOnPR: true,
      note: 'Close; also cover blank names in the test.',
      comments: [
        {
          path: 'internal/greeting/greeting.go',
          startLine: 10,
          line: 12,
          side: 'RIGHT',
          body: 'Name the fallback constant in the doc comment.',
        },
        { path: 'internal/greeting/greeting.go', line: 14, side: 'RIGHT' },
      ],
    });
    await expect(
      page.getByText(/Review queued \(2 comments\); DEMO-REV-1 moved to In Progress/),
    ).toBeVisible();
    await expect(dialog.getByTestId('review-draft-count')).toHaveText('0 comments');
  });

  test('unchecking both sends moveToActive and alsoPostOnPR false', async ({ page }) => {
    const sent: unknown[] = [];
    const dialog = await openChanges(page, 'DEMO-REV-1', sent);
    await draftReview(dialog);
    await dialog.getByRole('checkbox', { name: 'Move to In Progress' }).uncheck();
    await dialog.getByRole('checkbox', { name: 'Also post on the PR' }).uncheck();
    await expect(dialog.getByText(/without changing its tracker state/)).toBeVisible();
    await dialog.getByRole('button', { name: 'Send to agent' }).click();
    await expect.poll(() => sent.length).toBe(1);
    expect(sent[0]).toMatchObject({ moveToActive: false, alsoPostOnPR: false });
  });

  test('a live run hides "Move to"; the review goes to the running agent', async ({ page }) => {
    const sent: unknown[] = [];
    const dialog = await openChanges(page, 'DEMO-RUN-1', sent);
    await draftReview(dialog);
    await expect(dialog.getByRole('checkbox', { name: /Move to/ })).toHaveCount(0);
    await expect(
      dialog.getByText('The running agent gets the whole review as one message.'),
    ).toBeVisible();
    await dialog.getByRole('button', { name: 'Send to agent' }).click();
    await expect.poll(() => sent.length).toBe(1);
  });
});
