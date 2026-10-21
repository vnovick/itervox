// #203 — the Lessons tab: the review queue for proposed lessons (evidence,
// approve into a scope, edit then approve, reject) and the "not helping"
// filter for approved lessons.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { render } from '../../../test/render';
import { useToastStore } from '../../../store/toastStore';
import Memory from '../index';

vi.mock('../../../components/common/PageMeta', () => ({ default: () => null }));
vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../auth/authedFetch';

const T = '2026-10-01T12:00:00Z';

function entry(over: Record<string, unknown>) {
  return {
    id: 'm-1',
    type: 'lesson',
    text: 'When a feature needs CSV, reuse the csv helper instead of adding a dependency.',
    scope: 'private',
    status: 'proposed',
    created: T,
    updated: T,
    path: '/home/me/.itervox/memory.json',
    runs: [],
    ...over,
  };
}

const CSV = entry({
  source: {
    kind: 'lesson',
    issue: 'ENG-3',
    runIds: ['run-a', 'run-b'],
    links: ['https://github.com/acme/app/pull/9'],
  },
  lesson: {
    trigger: 'failed_then_succeeded',
    situation: 'a feature needs CSV',
    wentWrong: 'the earlier run failed',
    worked: 'the csv helper in src/lib',
    confidence: 0.7,
    evidence: 2,
    draftedBy: 'template',
  },
});
const TOKENS = entry({
  id: 'm-2',
  text: 'Never log the API token; redact it.',
  source: { kind: 'lesson', issue: 'ENG-4' },
  lesson: { trigger: 'steering', confidence: 0.5, evidence: 1 },
});
const STALE = entry({
  id: 'm-3',
  text: 'Rerun the SSE test once before failing.',
  status: 'active',
  notHelping: 'failing',
  notHelpingReason: '2 of the last 3 runs that used it failed',
});
const LESSONS = {
  enabled: true,
  drafter: 'profile:reviewer',
  scope: 'private',
  autoApprovePrivate: false,
  autoApproveProject: false,
  maxPerIssue: 3,
  rateWindowHours: 24,
  staleAfterRuns: 20,
  rejected: 4,
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
    lessons: LESSONS,
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

async function openLessons(user: ReturnType<typeof userEvent.setup>) {
  render(<Memory />);
  await user.click(await screen.findByRole('tab', { name: /Lessons/ }));
}

describe('Memory page, Lessons tab (#203)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useToastStore.setState({ toasts: [] });
  });

  it('lists proposals with their evidence, confidence and the lessons configuration', async () => {
    serve(() => view([CSV, TOKENS, STALE]));
    const user = userEvent.setup();
    await openLessons(user);
    const list = await screen.findByTestId('lessons-list');
    const card = within(list).getByTestId('lesson-m-1');
    expect(card).toHaveTextContent(CSV.text);
    expect(card).toHaveTextContent('not used until approved');
    const evidence = within(card).getByLabelText('Evidence');
    expect(evidence).toHaveTextContent('A failed run, then a successful one');
    expect(evidence).toHaveTextContent('a feature needs CSV');
    expect(evidence).toHaveTextContent('the earlier run failed');
    expect(evidence).toHaveTextContent('the csv helper in src/lib');
    expect(evidence).toHaveTextContent('ENG-3');
    expect(evidence).toHaveTextContent('run-a, run-b');
    expect(evidence).toHaveTextContent('70%');
    expect(evidence).toHaveTextContent('2×');
    expect(
      within(evidence).getByRole('link', { name: 'https://github.com/acme/app/pull/9' }),
    ).toHaveAttribute('href', 'https://github.com/acme/app/pull/9');
    expect(within(list).getByTestId('lesson-m-2')).toHaveTextContent('Correction in a steering');
    expect(within(list).queryByText(STALE.text)).toBeNull();
    expect(screen.getByTestId('lessons-config')).toHaveTextContent(
      'Drafted by reviewer · proposed into private memory · at most 3 per issue every 24h · every lesson waits for review · 4 rejected',
    );
    expect(screen.getByRole('tab', { name: /Lessons/ })).toHaveTextContent('2');
  });

  it('approves into the chosen scope; the lesson leaves the queue', async () => {
    let entries: unknown[] = [CSV, TOKENS];
    serve(
      () => view(entries),
      (url) => {
        if (url !== '/api/v1/memory/m-1/approve') return undefined;
        const approved = { ...CSV, status: 'active', scope: 'project' };
        entries = [approved, TOKENS];
        return json(approved);
      },
    );
    const user = userEvent.setup();
    await openLessons(user);
    const card = await screen.findByTestId('lesson-m-1');
    await user.selectOptions(within(card).getByLabelText('Approve into'), 'project');
    expect(card).toHaveTextContent('committed with the code');
    await user.click(within(card).getByRole('button', { name: 'Approve' }));
    await waitFor(() => {
      expect(posts()).toContainEqual({
        url: '/api/v1/memory/m-1/approve',
        body: { scope: 'project' },
      });
    });
    await waitFor(() => {
      expect(screen.queryByTestId('lesson-m-1')).toBeNull();
    });
    expect(screen.getByTestId('lesson-m-2')).toBeInTheDocument();
    expect(useToastStore.getState().toasts.map((t) => t.message)).toContain(
      'Lesson approved: runs use it from now on',
    );
  });

  it('edits then approves, sending the edited text', async () => {
    serve(
      () => view([TOKENS]),
      (url, init) => {
        if (url !== '/api/v1/memory/m-2/approve') return undefined;
        const body = JSON.parse(init?.body as string) as Record<string, unknown>;
        return json({ ...TOKENS, ...body, status: 'active' });
      },
    );
    const user = userEvent.setup();
    await openLessons(user);
    const card = await screen.findByTestId('lesson-m-2');
    await user.click(within(card).getByRole('button', { name: 'Edit' }));
    const text = within(card).getByLabelText('Lesson text');
    await user.clear(text);
    expect(within(card).getByRole('button', { name: 'Save and approve' })).toBeDisabled();
    await user.type(text, 'Redact tokens before logging.');
    await user.click(within(card).getByRole('button', { name: 'Save and approve' }));
    await waitFor(() => {
      expect(posts()).toContainEqual({
        url: '/api/v1/memory/m-2/approve',
        body: { scope: 'private', text: 'Redact tokens before logging.' },
      });
    });
  });

  it('rejects a proposal; a failed reject rolls back and says why', async () => {
    let fail = true;
    serve(
      () => view([CSV, TOKENS]),
      (url) => {
        if (url !== '/api/v1/memory/m-2/reject') return undefined;
        if (fail) return json({ error: { code: 'memory_conflict', message: 'not proposed' } }, 409);
        return json(TOKENS);
      },
    );
    const user = userEvent.setup();
    await openLessons(user);
    const card = await screen.findByTestId('lesson-m-2');
    await user.click(within(card).getByRole('button', { name: 'Reject' }));
    await waitFor(() => {
      expect(
        useToastStore.getState().toasts.some((t) => t.message.startsWith('Lesson not rejected')),
      ).toBe(true);
    });
    expect(await screen.findByTestId('lesson-m-2')).toBeInTheDocument();

    fail = false;
    await user.click(
      within(screen.getByTestId('lesson-m-2')).getByRole('button', { name: 'Reject' }),
    );
    await waitFor(() => {
      expect(posts().filter((p) => p.url === '/api/v1/memory/m-2/reject')).toHaveLength(2);
    });
  });

  it('lists approved lessons that are not helping, with the reason', async () => {
    serve(() => view([CSV, STALE]));
    const user = userEvent.setup();
    await openLessons(user);
    await user.click(await screen.findByRole('button', { name: 'Not helping (1)' }));
    const list = await screen.findByTestId('lessons-not-helping');
    expect(list).toHaveTextContent(STALE.text);
    expect(list).toHaveTextContent('Failing: 2 of the last 3 runs that used it failed');
    expect(list).not.toHaveTextContent(CSV.text);
  });

  it('shows the empty states', async () => {
    serve(() => ({ ...view([]), lessons: { ...LESSONS, enabled: false } }));
    const user = userEvent.setup();
    await openLessons(user);
    expect(await screen.findByTestId('lessons-empty')).toHaveTextContent('No proposed lessons');
    expect(screen.getByTestId('lessons-config')).toHaveTextContent('Lesson proposals are off');
    await user.click(screen.getByRole('button', { name: 'Not helping (0)' }));
    expect(screen.getByTestId('lessons-helping')).toHaveTextContent('none of the last 20 runs');
  });
});
