import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within, fireEvent, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import IssueChangesTab from '../IssueChangesTab';
import { routeFetch } from './fetchMock';
import { useReviewDraftStore } from '../../../../store/reviewDraftStore';

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
  files: [{ path: 'a.go', status: 'modified', additions: 2, deletions: 1 }],
};

const worktreeDiff = {
  ...prDiff,
  source: 'worktree',
  mergeBase: 'wtbase000000',
  files: [{ path: 'wip.go', status: 'untracked', additions: 1, deletions: 0 }],
};

const patch =
  'diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -10,2 +10,3 @@\n package a\n-old()\n+new()\n+more()\n';

const fileDiff = {
  identifier: 'ENG-1',
  base: 'main',
  file: prDiff.files[0],
  patch,
  bytes: patch.length,
  maxBytes: 262144,
  source: 'pr',
};

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

function routes(extra: Record<string, { status?: number; body: unknown }> = {}) {
  routeFetch(mockFetch, {
    '/api/v1/issues/ENG-1/diff': { body: prDiff },
    '/api/v1/issues/ENG-1/diff?source=worktree': { body: worktreeDiff },
    '/api/v1/issues/ENG-1/diff?file=a.go': { body: fileDiff },
    '/api/v1/issues/ENG-1/review-comments': {
      status: 202,
      body: { destination: 'agent', runId: 'run-1' },
    },
    '/api/v1/issues/ENG-1/pr-review': { status: 201, body: reviewResult },
    ...extra,
  });
}

/** The JSON bodies POSTed to `url`. */
function posted(url: string): Record<string, unknown>[] {
  return mockFetch.mock.calls
    .filter(([u, init]) => u === url && init?.method === 'POST')
    .map(([, init]) => JSON.parse(init?.body as string) as Record<string, unknown>);
}

async function openFile(live: boolean) {
  render(<IssueChangesTab identifier="ENG-1" live={live} />, { wrapper });
  await userEvent.click(await screen.findByRole('button', { name: /a\.go/ }));
  await screen.findByTestId('diff-viewer');
}

describe('change review (#238)', () => {
  beforeEach(() => {
    mockFetch.mockReset();
    useReviewDraftStore.setState({ pending: {} });
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
    await openFile(true);
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

  it('sends a comment to the running agent with the quoted lines', async () => {
    routes();
    await openFile(true);
    await userEvent.click(screen.getByRole('button', { name: 'Select base line 11' }));
    fireEvent.click(screen.getByRole('button', { name: 'Select line 11' }), { shiftKey: true });
    expect(screen.getByRole('radio', { name: /Send to the agent/ })).toBeChecked();
    await userEvent.type(screen.getByRole('textbox', { name: 'Comment' }), 'keep old()');
    await userEvent.click(screen.getByRole('button', { name: 'Send to agent' }));
    await waitFor(() => {
      expect(posted('/api/v1/issues/ENG-1/review-comments')).toHaveLength(1);
    });
    expect(posted('/api/v1/issues/ENG-1/review-comments')[0]).toEqual({
      destination: 'agent',
      path: 'a.go',
      line: 11,
      side: 'RIGHT',
      startLine: 11,
      startSide: 'LEFT',
      body: 'keep old()',
      quote: '-old()\n+new()',
    });
    await waitFor(() => {
      expect(screen.queryByTestId('line-comment-box')).toBeNull();
    });
  });

  it('offers only the PR when no run is live, and posts there now', async () => {
    routes({
      '/api/v1/issues/ENG-1/review-comments': {
        status: 201,
        body: { destination: 'pr', prUrl: PR_URL, url: `${PR_URL}#r1` },
      },
    });
    await openFile(false);
    await userEvent.click(screen.getByRole('button', { name: 'Select line 12' }));
    expect(screen.getByRole('radio', { name: /Send to the agent/ })).toBeDisabled();
    expect(screen.getByRole('radio', { name: /Post on the PR/ })).toBeChecked();
    await userEvent.type(screen.getByRole('textbox', { name: 'Comment' }), 'needs a test');
    await userEvent.click(screen.getByRole('button', { name: 'Post now' }));
    await waitFor(() => {
      expect(posted('/api/v1/issues/ENG-1/review-comments')).toEqual([
        { destination: 'pr', path: 'a.go', line: 12, side: 'RIGHT', body: 'needs a test' },
      ]);
    });
  });

  it('batches pending comments into one approve review and shows the merge policy', async () => {
    routes();
    await openFile(false);
    for (const [line, text] of [
      ['10', 'nit one'],
      ['12', 'nit two'],
    ] as const) {
      await userEvent.click(screen.getByRole('button', { name: `Select line ${line}` }));
      await userEvent.type(screen.getByRole('textbox', { name: 'Comment' }), text);
      await userEvent.click(screen.getByRole('button', { name: 'Add to review' }));
    }
    const list = screen.getByRole('list', { name: 'Pending comments' });
    expect(within(list).getAllByRole('listitem')).toHaveLength(2);
    expect(posted('/api/v1/issues/ENG-1/review-comments')).toHaveLength(0);

    await userEvent.click(
      screen.getByRole('button', { name: /Remove the comment on a\.go line 12/ }),
    );
    await userEvent.click(screen.getByRole('button', { name: 'Select line 11' }));
    await userEvent.type(screen.getByRole('textbox', { name: 'Comment' }), 'nit three');
    await userEvent.click(screen.getByRole('button', { name: 'Add to review' }));

    await userEvent.type(screen.getByRole('textbox', { name: 'Review summary' }), 'LGTM');
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
    expect(screen.queryByRole('list', { name: 'Pending comments' })).toBeNull();
  });

  it('needs a summary or a comment to request changes', async () => {
    routes();
    render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
    const panel = await screen.findByTestId('pending-review');
    expect(within(panel).getByRole('button', { name: 'Request changes' })).toBeDisabled();
    expect(within(panel).getByRole('button', { name: 'Approve' })).toBeEnabled();
    await userEvent.type(within(panel).getByRole('textbox'), 'Please split this');
    await userEvent.click(within(panel).getByRole('button', { name: 'Request changes' }));
    await waitFor(() => {
      expect(posted('/api/v1/issues/ENG-1/pr-review')[0]).toEqual({
        event: 'request_changes',
        body: 'Please split this',
        comments: [],
      });
    });
  });

  it('keeps viewers and older daemons out of review', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/diff': { body: { ...prDiff, hunksAllowed: false } },
    });
    render(<IssueChangesTab identifier="ENG-1" live />, { wrapper });
    expect(await screen.findByText('a.go')).toBeInTheDocument();
    expect(screen.queryByTestId('pending-review')).toBeNull();
  });
});
