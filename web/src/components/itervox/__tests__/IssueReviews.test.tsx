import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { IssueReviews } from '../IssueReviews';
import { IssueReviewSchema } from '../../../types/schemas';

// #135 — reviewer verdicts and the quorum in issue detail.
const closed = IssueReviewSchema.parse({
  verdicts: [
    {
      profile: 'reviewer-codex',
      verdict: 'block',
      reasons: ['missing test for the empty config'],
      comments: [{ path: 'internal/foo.go', line: 7, body: 'nil map write when cfg is empty' }],
      recordedAt: '2026-10-11T10:00:00Z',
    },
    { profile: 'reviewer', verdict: 'approve', recordedAt: '2026-10-11T10:05:00Z' },
  ],
  outcome: { blocked: true, approvals: 1, blocks: 1, quorum: 'any_block' },
});

describe('IssueReviews', () => {
  it('renders nothing without a review', () => {
    const { container } = render(<IssueReviews />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders each verdict with its reasons and line comments, and the quorum', () => {
    render(<IssueReviews review={closed} />);
    const section = screen.getByRole('region', { name: 'Review' });
    expect(within(section).getByText('reviewer-codex')).toBeInTheDocument();
    expect(within(section).getByText('Changes requested')).toBeInTheDocument();
    expect(within(section).getByText('missing test for the empty config')).toBeInTheDocument();
    expect(within(section).getByText('internal/foo.go:7')).toBeInTheDocument();
    expect(within(section).getByText('nil map write when cfg is empty')).toBeInTheDocument();
    expect(within(section).getByText('reviewer')).toBeInTheDocument();
    expect(within(section).getByText('Approved')).toBeInTheDocument();
    expect(
      within(section).getByText(/Blocked — 1 approve \/ 1 block \(any_block\)/),
    ).toBeInTheDocument();
  });

  it('shows a review in progress without a quorum and disables re-running it', () => {
    const onRerun = vi.fn();
    render(
      <IssueReviews
        review={{ verdicts: [closed.verdicts[0]], inProgress: true }}
        onRerun={onRerun}
      />,
    );
    expect(screen.getByText('Reviewing…')).toBeInTheDocument();
    expect(screen.queryByText(/approve \//)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Re-run review' })).toBeDisabled();
  });

  it('re-runs the review', () => {
    const onRerun = vi.fn();
    render(<IssueReviews review={closed} onRerun={onRerun} />);
    fireEvent.click(screen.getByRole('button', { name: 'Re-run review' }));
    expect(onRerun).toHaveBeenCalledTimes(1);
  });

  it('shows the pending state and hides the action when it is not offered', () => {
    const { rerender } = render(<IssueReviews review={closed} onRerun={vi.fn()} rerunPending />);
    expect(screen.getByRole('button', { name: 'Starting…' })).toBeDisabled();
    rerender(<IssueReviews review={closed} />);
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('renders repeated reasons and comments without key collisions', () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {});
    render(
      <IssueReviews
        review={{
          verdicts: [
            {
              profile: 'reviewer',
              verdict: 'block',
              reasons: ['same', 'same'],
              comments: [
                { path: 'a.go', line: 1, body: 'dup' },
                { path: 'a.go', line: 1, body: 'dup' },
              ],
              recordedAt: '2026-10-11T10:00:00Z',
            },
          ],
        }}
      />,
    );
    expect(screen.getAllByText('same')).toHaveLength(2);
    expect(screen.getAllByText('dup')).toHaveLength(2);
    expect(error.mock.calls.flat().join(' ')).not.toMatch(/same key/);
    error.mockRestore();
  });
});
