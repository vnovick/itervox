import { describe, it, expect, vi, beforeEach, beforeAll } from 'vitest';
import { render, screen, within, fireEvent, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import IssueChangesTab from '../IssueChangesTab';
import { routeFetch } from './fetchMock';
import { useReviewDraftStore } from '../../../../store/reviewDraftStore';
import { useDiffPrefsStore } from '../../../../store/diffPrefsStore';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { useToastStore } from '../../../../store/toastStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../../auth/authedFetch';
const mockFetch = vi.mocked(authedFetch);

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const PR_URL = 'https://github.com/acme/app/pull/7';

const prDiff = {
  identifier: 'ENG-1',
  base: 'main',
  mergeBase: 'basesha1',
  headSha: 'headsha1234567',
  hunksAllowed: true,
  source: 'pr',
  sources: ['pr', 'worktree'],
  prUrl: PR_URL,
  worktreePath: '/work/ENG-1',
  files: [
    { path: 'a.go', status: 'modified', additions: 2, deletions: 1 },
    { path: 'b.go', status: 'modified', additions: 1, deletions: 0 },
  ],
};

const worktreeDiff = {
  ...prDiff,
  source: 'worktree',
  mergeBase: 'wtbase000000',
  files: [{ path: 'wip.go', status: 'untracked', additions: 1, deletions: 0 }],
};

const patch =
  'diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -10,2 +10,3 @@\n package a\n-old()\n+new()\n+more()\n';
const patchB = 'diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,1 +1,2 @@\n x\n+y()\n';

const fileDiff = (file: (typeof prDiff.files)[number], p: string) => ({
  identifier: 'ENG-1',
  base: 'main',
  file,
  patch: p,
  bytes: p.length,
  maxBytes: 262144,
  source: 'pr',
});

const reviewResult = {
  prUrl: PR_URL,
  id: 202,
  event: 'approve',
  comments: 2,
  merged: false,
  mergePolicy: {
    mergesFromHere: false,
    blockLabels: ['needs-human'],
    quorum: { blocked: true, approvals: 1, blocks: 1, quorum: 'any_block' },
  },
};

const CONTINUE = '/api/v1/issues/ENG-1/review-continue';

function routes(extra: Record<string, { status?: number; body: unknown }> = {}) {
  routeFetch(mockFetch, {
    '/api/v1/issues/ENG-1/diff': { body: prDiff },
    '/api/v1/issues/ENG-1/diff?source=worktree': { body: worktreeDiff },
    '/api/v1/issues/ENG-1/diff?file=a.go': { body: fileDiff(prDiff.files[0], patch) },
    '/api/v1/issues/ENG-1/diff?file=b.go': { body: fileDiff(prDiff.files[1], patchB) },
    '/api/v1/issues/ENG-1/pr-review': { status: 201, body: reviewResult },
    [CONTINUE]: {
      status: 202,
      body: { agent: { delivery: 'steered', runId: 'run-1', messages: 1 } },
    },
    ...extra,
  });
}

/** The JSON bodies POSTed to `url`. */
function posted(url: string): Record<string, unknown>[] {
  return mockFetch.mock.calls
    .filter(([u, init]) => u === url && init?.method === 'POST')
    .map(([, init]) => JSON.parse(init?.body as string) as Record<string, unknown>);
}

function toastMessages(): string[] {
  return useToastStore.getState().toasts.map((t) => t.message);
}

async function openFile(props: { live?: boolean; issueState?: string } = {}) {
  const view = render(<IssueChangesTab identifier="ENG-1" {...props} />, { wrapper });
  await userEvent.click(await screen.findByRole('button', { name: /^M ?a\.go/ }));
  await screen.findByTestId('diff-viewer');
  return view;
}

async function comment(line: string, text: string) {
  await userEvent.click(screen.getByRole('button', { name: line }));
  await userEvent.type(screen.getByRole('textbox', { name: 'Comment' }), text);
  await userEvent.click(screen.getByRole('button', { name: 'Add to review' }));
}

const scrollIntoView = vi.fn();
beforeAll(() => {
  Element.prototype.scrollIntoView = scrollIntoView;
});

describe('change review (#238) and review and continue', () => {
  beforeEach(() => {
    mockFetch.mockReset();
    useReviewDraftStore.setState({ pending: {}, notes: {}, viewed: {} });
    useDiffPrefsStore.setState({ layout: 'unified' });
    useItervoxStore.setState({ snapshot: makeSnapshot({ activeStates: ['Todo', 'In Progress'] }) });
    useToastStore.setState({ toasts: [] });
    localStorage.clear();
  });

  it('shows the PR files by default and switches to the live worktree', async () => {
    routes();
    render(<IssueChangesTab identifier="ENG-1" live />, { wrapper });
    expect(await screen.findByText(/Pull request against/)).toHaveTextContent('headsha12345');
    const group = screen.getByRole('group', { name: 'Diff source' });
    expect(within(group).getByRole('button', { name: 'PR files' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    await userEvent.click(within(group).getByRole('button', { name: 'Live worktree' }));
    expect(await screen.findByText('wip.go')).toBeInTheDocument();
    expect(screen.getByText(/merge base/)).toHaveTextContent('wtbase000000');
  });

  it('selects a line, extends it to a range with shift-click and opens the comment box', async () => {
    routes();
    await openFile({ live: true });
    await userEvent.click(screen.getByRole('button', { name: 'Select line 11' }));
    expect(screen.getByTestId('line-comment-box')).toHaveTextContent('line 11');
    fireEvent.click(screen.getByRole('button', { name: 'Select line 12' }), { shiftKey: true });
    const box = screen.getByTestId('line-comment-box');
    expect(box).toHaveTextContent('lines 11–12');
    expect(screen.getByRole('button', { name: 'Select line 12' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    await userEvent.click(within(box).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByTestId('line-comment-box')).toBeNull();
  });

  it('selects a range by dragging across line numbers', async () => {
    routes();
    await openFile();
    fireEvent.mouseDown(screen.getByRole('button', { name: 'Select line 10' }), { button: 0 });
    fireEvent.mouseEnter(screen.getByRole('button', { name: 'Select line 11' }), { buttons: 1 });
    fireEvent.mouseEnter(screen.getByRole('button', { name: 'Select line 12' }), { buttons: 1 });
    fireEvent.mouseUp(window);
    // A later hover without the button held does not extend it.
    fireEvent.mouseEnter(screen.getByRole('button', { name: 'Select line 12' }), { buttons: 0 });
    expect(screen.getByTestId('line-comment-box')).toHaveTextContent('lines 10–12');
  });

  it('collects comments and a note into a draft that survives switching files and remounting', async () => {
    routes();
    const view = await openFile();
    await comment('Select line 10', 'nit one');
    await comment('Select line 12', 'nit two');
    expect(screen.getAllByTestId('diff-draft-comment').map((n) => n.textContent)).toEqual([
      'nit one',
      'nit two',
    ]);
    await userEvent.click(screen.getByRole('button', { name: /^M ?b\.go/ }));
    await screen.findByRole('button', { name: 'Select line 2' });
    await comment('Select line 2', 'on b');
    await userEvent.type(screen.getByRole('textbox', { name: 'Overall note' }), 'Overall fine');
    view.unmount();

    await openFile();
    const list = screen.getByRole('list', { name: 'Draft comments' });
    expect(
      within(list)
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual([
      expect.stringContaining('nit one'),
      expect.stringContaining('nit two'),
      expect.stringContaining('on b'),
    ]);
    expect(screen.getByRole('textbox', { name: 'Overall note' })).toHaveValue('Overall fine');
    expect(screen.getByTestId('review-draft-count')).toHaveTextContent('3 comments');
    const files = screen.getByRole('list', { name: 'Changed files' });
    expect(within(files).getByTitle('2 draft comments')).toBeInTheDocument();
  });

  it('sends the whole draft to the live agent in one request and clears it', async () => {
    routes();
    await openFile({ live: true });
    await userEvent.click(screen.getByRole('button', { name: 'Select base line 11' }));
    fireEvent.click(screen.getByRole('button', { name: 'Select line 11' }), { shiftKey: true });
    await userEvent.type(screen.getByRole('textbox', { name: 'Comment' }), 'keep old()');
    await userEvent.click(screen.getByRole('button', { name: 'Add to review' }));
    await userEvent.type(screen.getByRole('textbox', { name: 'Overall note' }), 'Close.');
    expect(screen.queryByRole('checkbox', { name: /Move to/ })).toBeNull();
    expect(screen.getByRole('checkbox', { name: 'Also post on the PR' })).toBeChecked();
    await userEvent.click(screen.getByRole('button', { name: 'Send to agent' }));
    await waitFor(() => {
      expect(posted(CONTINUE)).toHaveLength(1);
    });
    expect(posted(CONTINUE)[0]).toEqual({
      comments: [
        {
          path: 'a.go',
          line: 11,
          side: 'RIGHT',
          startLine: 11,
          startSide: 'LEFT',
          body: 'keep old()',
          quote: '-old()\n+new()',
        },
      ],
      note: 'Close.',
      moveToActive: true,
      alsoPostOnPR: true,
    });
    await waitFor(() => {
      expect(screen.getByTestId('review-draft-count')).toHaveTextContent('0 comments');
    });
    expect(toastMessages()).toContain('Review sent to the running agent (1 comment)');
  });

  it('with no live run offers "Move to <first active state>" and "Also post on the PR", both on', async () => {
    routes({
      [CONTINUE]: {
        status: 202,
        body: {
          agent: { delivery: 'queued', queuedAt: '2026-10-31T12:00:00Z' },
          pr: { posted: false, error: 'github_rejected', message: 'GitHub refused it: line' },
        },
      },
    });
    await openFile({ issueState: 'In Review' });
    await comment('Select line 12', 'needs a test');
    const move = screen.getByRole('checkbox', { name: 'Move to Todo' });
    const pr = screen.getByRole('checkbox', { name: 'Also post on the PR' });
    expect(move).toBeChecked();
    expect(pr).toBeChecked();
    expect(screen.getByText(/ENG-1 moves to Todo and a new run continues/)).toBeInTheDocument();
    await userEvent.click(move);
    await userEvent.click(pr);
    expect(screen.getByText(/without changing its tracker state/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Send to agent' }));
    await waitFor(() => {
      expect(posted(CONTINUE)).toHaveLength(1);
    });
    expect(posted(CONTINUE)[0]).toMatchObject({ moveToActive: false, alsoPostOnPR: false });
    await waitFor(() => {
      expect(toastMessages()).toContain(
        "Review queued (1 comment); a run starts on ENG-1's branch when admitted",
      );
    });
    expect(toastMessages()).toContain('Not posted on the PR: GitHub refused it: line');
  });

  it('hides "Move to" for an issue already in an active state, and the PR box without a PR', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/diff': {
        body: { ...prDiff, source: 'worktree', sources: ['worktree'], prUrl: undefined },
      },
      '/api/v1/issues/ENG-1/diff?file=a.go': { body: fileDiff(prDiff.files[0], patch) },
    });
    render(<IssueChangesTab identifier="ENG-1" issueState="in progress" />, { wrapper });
    expect(await screen.findByTestId('review-draft')).toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /Move to/ })).toBeNull();
    expect(screen.queryByRole('checkbox', { name: 'Also post on the PR' })).toBeNull();
    expect(screen.queryByTestId('pending-review')).toBeNull();
    expect(screen.getByRole('button', { name: 'Send to agent' })).toBeDisabled();
  });

  it('shows a queued review from the snapshot', async () => {
    routes();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        activeStates: ['Todo'],
        queuedReviews: [
          {
            identifier: 'ENG-1',
            comments: 2,
            moveToActive: false,
            queuedAt: '2026-10-31T12:00:00Z',
          },
        ],
      }),
    });
    render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
    expect(await screen.findByTestId('review-queued')).toHaveTextContent(
      'A review (2 comments) is queued for the next run of ENG-1',
    );
  });

  it('batches the draft into one approve review and shows the merge policy', async () => {
    routes();
    await openFile();
    await comment('Select line 10', 'nit one');
    await comment('Select line 12', 'nit two');
    await userEvent.click(
      screen.getByRole('button', { name: /Remove the comment on a\.go line 12/ }),
    );
    await comment('Select line 11', 'nit three');
    await userEvent.type(screen.getByRole('textbox', { name: 'Overall note' }), 'LGTM');
    await userEvent.click(screen.getByRole('button', { name: 'Approve' }));
    await waitFor(() => {
      expect(posted('/api/v1/issues/ENG-1/pr-review')).toHaveLength(1);
    });
    expect(posted('/api/v1/issues/ENG-1/pr-review')[0]).toEqual({
      event: 'approve',
      body: 'LGTM',
      comments: [
        { path: 'a.go', line: 10, side: 'RIGHT', body: 'nit one' },
        { path: 'a.go', line: 11, side: 'RIGHT', body: 'nit three' },
      ],
    });
    const note = await screen.findByTestId('review-policy');
    expect(note).toHaveTextContent('Itervox does not merge from here');
    expect(note).toHaveTextContent('blocked by label: needs-human');
    expect(note).toHaveTextContent('Agent review quorum: blocked');
    expect(screen.queryByRole('list', { name: 'Draft comments' })).toBeNull();
    expect(posted(CONTINUE)).toHaveLength(0);
  });

  it('needs a note or a comment to request changes', async () => {
    routes();
    render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
    const panel = await screen.findByTestId('pending-review');
    expect(within(panel).getByRole('button', { name: 'Request changes' })).toBeDisabled();
    expect(within(panel).getByRole('button', { name: 'Approve' })).toBeEnabled();
    await userEvent.type(screen.getByRole('textbox', { name: 'Overall note' }), 'Please split');
    await userEvent.click(within(panel).getByRole('button', { name: 'Request changes' }));
    await waitFor(() => {
      expect(posted('/api/v1/issues/ENG-1/pr-review')[0]).toEqual({
        event: 'request_changes',
        body: 'Please split',
        comments: [],
      });
    });
  });

  it('toggles side by side, remembers it, and pairs deletions with additions', async () => {
    routes();
    await openFile();
    await userEvent.click(screen.getByRole('button', { name: 'Side by side' }));
    const viewer = screen.getByTestId('diff-viewer');
    expect(viewer).toHaveAttribute('data-layout', 'split');
    expect(localStorage.getItem('itervox.diffLayout')).toBe('split');
    // -old() and +new() share a row; +more() has no base line.
    const oldRow = within(viewer).getByText('-old()').closest('.grid');
    expect(oldRow).toHaveTextContent('+new()');
    expect(within(viewer).getByRole('button', { name: 'Select base line 11' })).toBeInTheDocument();
    await comment('Select line 12', 'split note');
    expect(screen.getByTestId('diff-draft-comment')).toHaveTextContent('split note');
    await userEvent.click(screen.getByRole('button', { name: 'Unified' }));
    expect(screen.getByTestId('diff-viewer')).toHaveAttribute('data-layout', 'unified');
  });

  it('filters files, ticks them viewed, and steps with j/k and n/p', async () => {
    routes();
    await openFile();
    await comment('Select line 10', 'first');
    const tab = screen.getByTestId('issue-changes-tab');

    await userEvent.click(screen.getByRole('checkbox', { name: 'Viewed a.go' }));
    expect(screen.getByTestId('viewed-count')).toHaveTextContent('1/2 viewed');

    fireEvent.keyDown(screen.getByRole('button', { name: /^M ?a\.go/ }), { key: 'j' });
    expect(await screen.findByRole('button', { name: 'Select line 2' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^M ?b\.go/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    fireEvent.keyDown(tab, { key: 'k' });
    expect(screen.getByRole('button', { name: /^M ?a\.go/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    );

    fireEvent.keyDown(tab, { key: 'j' });
    fireEvent.keyDown(tab, { key: 'n' });
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /^M ?a\.go/ })).toHaveAttribute(
        'aria-pressed',
        'true',
      );
    });
    await waitFor(() => {
      expect(scrollIntoView).toHaveBeenCalled();
    });
    // Typing in a field never triggers a shortcut.
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Overall note' }), { key: 'j' });
    expect(screen.getByRole('button', { name: /^M ?a\.go/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    );

    await userEvent.type(screen.getByRole('searchbox', { name: 'Filter files' }), 'b.g');
    const files = screen.getByRole('list', { name: 'Changed files' });
    expect(within(files).queryByText('a.go')).toBeNull();
    expect(within(files).getByText('b.go')).toBeInTheDocument();
    expect(screen.getByTestId('diff-keys')).toHaveTextContent('Keys: j/k file, n/p comment');
  });

  it('keeps viewers and older daemons out of review', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/diff': { body: { ...prDiff, hunksAllowed: false } },
    });
    render(<IssueChangesTab identifier="ENG-1" live />, { wrapper });
    expect(await screen.findByText('a.go')).toBeInTheDocument();
    expect(screen.queryByTestId('review-draft')).toBeNull();
    expect(screen.queryByTestId('diff-toolbar')).toBeNull();
  });
});
