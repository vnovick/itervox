import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { IssueBlockerDetails } from '../IssueBlockerDetails';
import type { DependencyAttentionRow } from '../../../types/schemas';

describe('IssueBlockerDetails', () => {
  it('renders nothing when there is no ineligible reason, no blockers, and no attention entry', () => {
    const { container } = render(<IssueBlockerDetails issue={{}} />);
    expect(container.firstChild).toBeNull();
  });

  it('renders the ineligible reason and blocker chips as before', () => {
    render(
      <IssueBlockerDetails
        issue={{
          ineligibleReason: 'blocked_by_unresolved_dependency',
          blockedByDetails: [{ identifier: 'ENG-1', state: 'In Progress' }],
        }}
      />,
    );
    expect(screen.getByText('blocked_by_unresolved_dependency')).toBeInTheDocument();
    expect(screen.getByText('ENG-1')).toBeInTheDocument();
  });

  // critical-path-ordering Task 5/6 — the attention badge is a new,
  // independent trigger for rendering: an issue can have zero blockers and
  // no ineligible reason yet still need an operator's attention (e.g. it is
  // itself the tail of a cycle another issue points back into).
  it('renders the needs-attention badge for a cycle entry even with no blockers', () => {
    const attention: DependencyAttentionRow = {
      identifier: 'ENG-1',
      blockers: ['ENG-2'],
      blockedSince: '2026-05-25T09:00:00Z',
      kind: 'cycle',
    };
    render(<IssueBlockerDetails issue={{}} attention={attention} />);
    const badge = screen.getByTestId('issue-attention-badge');
    expect(badge).toHaveTextContent('Needs attention');
    expect(badge).toHaveTextContent('dependency cycle');
  });

  // critical-path-ordering / #51 — a dependency cycle is a hard blocker
  // (nothing in the cycle can ever become dispatchable without operator
  // intervention), so it keeps danger severity like the pre-existing blocker
  // chips.
  it('uses danger styling for a cycle entry', () => {
    const attention: DependencyAttentionRow = {
      identifier: 'ENG-1',
      blockers: ['ENG-2'],
      blockedSince: '2026-05-25T09:00:00Z',
      kind: 'cycle',
    };
    render(<IssueBlockerDetails issue={{}} attention={attention} />);
    const badge = screen.getByTestId('issue-attention-badge');
    expect(badge.className).toContain('bg-theme-danger-soft');
    expect(badge.className).toContain('text-theme-danger');
    expect(badge.className).not.toContain('bg-theme-warning-soft');
    expect(badge.className).not.toContain('text-theme-warning');
  });

  it('renders the stale-blocker wording for a stale_blocker entry', () => {
    const attention: DependencyAttentionRow = {
      identifier: 'ENG-1',
      blockers: ['ENG-2'],
      blockedSince: '2026-05-20T09:00:00Z',
      kind: 'stale_blocker',
    };
    render(<IssueBlockerDetails issue={{}} attention={attention} />);
    const badge = screen.getByTestId('issue-attention-badge');
    expect(badge).toHaveTextContent('Needs attention');
    expect(badge).toHaveTextContent('stale blocker');
  });

  // critical-path-ordering / #51 — a stale blocker is recoverable (it just
  // needs the blocker issue to move), so it's lower severity than a cycle.
  // Matches LiveOpsStrip's danger-vs-warning vocabulary for dependency
  // attention (see LiveOpsStrip.tsx's cycleCount-vs-attentionCount OpsChip).
  it('uses warning styling for a stale_blocker entry', () => {
    const attention: DependencyAttentionRow = {
      identifier: 'ENG-1',
      blockers: ['ENG-2'],
      blockedSince: '2026-05-20T09:00:00Z',
      kind: 'stale_blocker',
    };
    render(<IssueBlockerDetails issue={{}} attention={attention} />);
    const badge = screen.getByTestId('issue-attention-badge');
    expect(badge.className).toContain('bg-theme-warning-soft');
    expect(badge.className).toContain('text-theme-warning');
    expect(badge.className).not.toContain('bg-theme-danger-soft');
    expect(badge.className).not.toContain('text-theme-danger');
  });

  it('renders the attention badge alongside blocker chips when both are present', () => {
    const attention: DependencyAttentionRow = {
      identifier: 'ENG-1',
      blockers: ['ENG-2'],
      blockedSince: '2026-05-25T09:00:00Z',
      kind: 'cycle',
    };
    render(
      <IssueBlockerDetails
        issue={{ blockedByDetails: [{ identifier: 'ENG-2', state: 'Backlog' }] }}
        attention={attention}
      />,
    );
    expect(screen.getByTestId('issue-attention-badge')).toBeInTheDocument();
    expect(screen.getByText('ENG-2')).toBeInTheDocument();
  });

  it('omits the attention badge when the issue has no matching entry', () => {
    render(
      <IssueBlockerDetails
        issue={{ blockedByDetails: [{ identifier: 'ENG-2', state: 'Backlog' }] }}
      />,
    );
    expect(screen.queryByTestId('issue-attention-badge')).not.toBeInTheDocument();
  });
});
