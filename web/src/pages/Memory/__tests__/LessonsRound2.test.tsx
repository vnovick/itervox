// #217 — Lessons round 2 on the Lessons tab: approved lessons with their
// analytics, promotion to a rule, and the enforcement suggestion card
// (create the Enforce: issue, or dismiss), plus the "enforced" state under
// Not helping.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { render } from '../../../test/render';
import { useToastStore } from '../../../store/toastStore';
import { useUIStore } from '../../../store/uiStore';
import Memory from '../index';

vi.mock('../../../components/common/PageMeta', () => ({ default: () => null }));
vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../auth/authedFetch';

const T = '2026-10-01T12:00:00Z';

const SUGGESTION = {
  kind: 'lint',
  status: 'suggested',
  title: 'Ban new CSV dependencies',
  draft: 'Add an eslint no-restricted-imports rule for csv packages.',
  draftedBy: 'profile:reviewer',
  suggestedAt: T,
};

const APPROVED = {
  id: 'm-1',
  type: 'lesson',
  text: 'When a feature needs CSV, reuse the csv helper instead of adding a dependency.',
  scope: 'project',
  status: 'active',
  created: T,
  updated: T,
  path: '/repo/.itervox/memory.json',
  runs: [],
  useCount: 5,
  source: { kind: 'lesson', issue: 'ENG-3' },
  lesson: { trigger: 'reviewer_ask', evidence: 3, enforcement: SUGGESTION },
  analytics: { applied: 5, runs: 4, succeeded: 3, failed: 1 },
};

const RECIPE = {
  ...APPROVED,
  id: 'm-2',
  text: 'Recipe: when tests flake, use a fake clock. Verify with: go test -count=20.',
  status: 'proposed',
  scope: 'private',
  lesson: { trigger: 'recipe', evidence: 1, verification: 'go test -count=20 passes' },
  analytics: undefined,
};

const ENFORCED = {
  ...APPROVED,
  id: 'm-3',
  text: 'Never log the API token; redact it.',
  lesson: {
    trigger: 'steering',
    evidence: 4,
    enforcement: { ...SUGGESTION, status: 'accepted', issue: 'ENG-9', issueUrl: 'https://t/ENG-9' },
  },
  notHelping: 'enforced',
  notHelpingReason: 'enforced by ENG-9',
  analytics: { applied: 0, runs: 0, succeeded: 0, failed: 0 },
};

function view(entries: unknown[]) {
  return {
    enabled: true,
    tokenBudget: 2000,
    promptShareTokens: 8000,
    schemaUrl: 'https://itervox.dev/schema/memory.schema.json',
    files: [
      { scope: 'private', path: '/home/me/.itervox/memory.json', exists: true, writable: true },
      { scope: 'project', path: '/repo/.itervox/memory.json', exists: true, writable: true },
    ],
    entries,
    lessons: {
      enabled: true,
      drafter: 'template',
      scope: 'private',
      autoApprovePrivate: false,
      autoApproveProject: false,
      maxPerIssue: 3,
      rateWindowHours: 24,
      staleAfterRuns: 20,
      rejected: 0,
      enforceThreshold: 3,
    },
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  return input instanceof URL ? input.href : input.url;
}

type Handler = (url: string, init?: RequestInit) => Response | undefined;

function serve(current: () => unknown, write: Handler = () => undefined) {
  vi.mocked(authedFetch).mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = urlOf(input);
    const method = init?.method ?? 'GET';
    if (method === 'GET' && url === '/api/v1/memory') return Promise.resolve(json(current()));
    return Promise.resolve(write(url, init) ?? json({}, 404));
  });
}

function posts() {
  return vi
    .mocked(authedFetch)
    .mock.calls.filter(([, init]) => init?.method === 'POST')
    .map(([input, init]) => ({
      url: urlOf(input),
      body: typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined,
    }));
}

async function openApproved(user: ReturnType<typeof userEvent.setup>) {
  render(<Memory />);
  await user.click(await screen.findByRole('tab', { name: /Lessons/ }));
  await user.click(await screen.findByRole('button', { name: /^Approved \(/ }));
}

describe('Memory page, Lessons round 2 (#217)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useToastStore.setState({ toasts: [] });
    useUIStore.setState({ memoryTab: 'entries', memoryLessonsView: 'queue' });
  });

  it('shows each approved lesson with its analytics', async () => {
    serve(() => view([APPROVED, RECIPE, ENFORCED]));
    const user = userEvent.setup();
    await openApproved(user);
    const list = await screen.findByTestId('lessons-approved');
    expect(within(list).queryByText(RECIPE.text)).toBeNull();
    const card = within(list).getByTestId('approved-lesson-m-1');
    expect(card).toHaveTextContent('A change reviewers asked for more than once');
    expect(within(card).getByTestId('analytics-m-1')).toHaveTextContent(
      'Applied 5× · 4 runs in history: 3 succeeded, 1 failed',
    );
    expect(within(list).getByTestId('analytics-m-3')).toHaveTextContent(
      'Applied 0× · 0 runs in history',
    );
    expect(screen.getByTestId('lessons-config')).toHaveTextContent(
      'enforcement suggested after 3 sightings',
    );
  });

  it('promotes an approved lesson to a rule in the chosen layer', async () => {
    let entries: unknown[] = [APPROVED];
    serve(
      () => view(entries),
      (url) => {
        if (url !== '/api/v1/memory/m-1/promote-to-rule') return undefined;
        const archived = {
          ...APPROVED,
          status: 'archived',
          lesson: { ...APPROVED.lesson, rule: { layer: 'user', id: 'lesson-m-1', at: T } },
        };
        entries = [archived];
        return json({
          entry: archived,
          layer: 'user',
          path: '/home/me/.itervox/rules.json',
          rule: { id: 'lesson-m-1', severity: 'must', text: APPROVED.text },
        });
      },
    );
    const user = userEvent.setup();
    await openApproved(user);
    const form = await screen.findByTestId('promote-rule-m-1');
    await user.selectOptions(within(form).getByLabelText('Rules layer'), 'user');
    await user.selectOptions(within(form).getByLabelText('Severity'), 'must');
    await user.click(within(form).getByRole('button', { name: 'Promote to rule' }));
    await waitFor(() => {
      expect(posts()).toContainEqual({
        url: '/api/v1/memory/m-1/promote-to-rule',
        body: { layer: 'user', severity: 'must' },
      });
    });
    await waitFor(() => {
      expect(useToastStore.getState().toasts.map((t) => t.message)).toContain(
        'Rule lesson-m-1 added to the user layer; the lesson is archived',
      );
    });
    await waitFor(() => {
      expect(screen.queryByTestId('approved-lesson-m-1')).toBeNull();
    });
  });

  it('shows the enforcement suggestion and creates the Enforce issue', async () => {
    let entries: unknown[] = [APPROVED];
    serve(
      () => view(entries),
      (url) => {
        if (url !== '/api/v1/memory/m-1/enforcement/accept') return undefined;
        const accepted = {
          ...APPROVED,
          lesson: {
            ...APPROVED.lesson,
            enforcement: {
              ...SUGGESTION,
              status: 'accepted',
              issue: 'ENG-9',
              issueUrl: 'https://t/ENG-9',
            },
          },
        };
        entries = [accepted];
        return json({
          entry: accepted,
          issue: {
            identifier: 'ENG-9',
            title: `Enforce: ${APPROVED.text}`,
            url: 'https://t/ENG-9',
          },
        });
      },
    );
    const user = userEvent.setup();
    await openApproved(user);
    const card = await screen.findByTestId('enforcement-m-1');
    expect(card).toHaveTextContent('Suggested enforcement · Lint rule');
    expect(card).toHaveTextContent('Ban new CSV dependencies');
    expect(card).toHaveTextContent('no-restricted-imports');
    expect(card).toHaveTextContent('Seen 3× · drafted by profile:reviewer');
    expect(card).toHaveTextContent('never commits code');
    await user.click(within(card).getByRole('button', { name: 'Create Enforce issue' }));
    await waitFor(() => {
      expect(posts().map((p) => p.url)).toContain('/api/v1/memory/m-1/enforcement/accept');
    });
    const tracked = await screen.findByTestId('enforcement-issue');
    expect(tracked).toHaveTextContent('Tracked in ENG-9');
    expect(within(tracked).getByRole('link', { name: 'ENG-9' })).toHaveAttribute(
      'href',
      'https://t/ENG-9',
    );
    expect(useToastStore.getState().toasts.map((t) => t.message)).toContain(
      `Created ENG-9: Enforce: ${APPROVED.text}`,
    );
  });

  it('dismisses a suggestion; a failed create says why', async () => {
    let entries: unknown[] = [APPROVED];
    serve(
      () => view(entries),
      (url) => {
        if (url === '/api/v1/memory/m-1/enforcement/accept') {
          return json(
            { error: { code: 'enforcement_issue_failed', message: 'tracker down' } },
            502,
          );
        }
        if (url === '/api/v1/memory/m-1/enforcement/dismiss') {
          const dismissed = {
            ...APPROVED,
            lesson: { ...APPROVED.lesson, enforcement: { ...SUGGESTION, status: 'dismissed' } },
          };
          entries = [dismissed];
          return json(dismissed);
        }
        return undefined;
      },
    );
    const user = userEvent.setup();
    await openApproved(user);
    const card = await screen.findByTestId('enforcement-m-1');
    await user.click(within(card).getByRole('button', { name: 'Create Enforce issue' }));
    await waitFor(() => {
      expect(
        useToastStore
          .getState()
          .toasts.some((t) => t.message.startsWith('Enforcement issue not created')),
      ).toBe(true);
    });
    await user.click(within(card).getByRole('button', { name: 'Dismiss' }));
    await waitFor(() => {
      expect(screen.queryByTestId('enforcement-m-1')).toBeNull();
    });
    expect(posts().map((p) => p.url)).toContain('/api/v1/memory/m-1/enforcement/dismiss');
  });

  it('shows a recipe proposal with its verification, and enforced lessons under Not helping', async () => {
    serve(() => view([APPROVED, RECIPE, ENFORCED]));
    const user = userEvent.setup();
    render(<Memory />);
    await user.click(await screen.findByRole('tab', { name: /Lessons/ }));
    const proposal = await screen.findByTestId('lesson-m-2');
    const evidence = within(proposal).getByLabelText('Evidence');
    expect(evidence).toHaveTextContent('A hard-won success (recipe)');
    expect(evidence).toHaveTextContent('go test -count=20 passes');
    await user.click(screen.getByRole('button', { name: 'Not helping (1)' }));
    const list = await screen.findByTestId('lessons-not-helping');
    expect(list).toHaveTextContent('Enforced: enforced by ENG-9');
    expect(list).not.toHaveTextContent(APPROVED.text);
  });
});
