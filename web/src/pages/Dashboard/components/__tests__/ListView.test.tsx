import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ListView } from '../ListView';
import type { TrackerIssue } from '../../../../types/schemas';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';

vi.mock('../../../../queries/issues', () => ({
  useCancelIssue: () => ({ mutate: vi.fn(), isPending: false }),
  useResumeIssue: () => ({ mutate: vi.fn(), isPending: false }),
}));

const baseIssue: TrackerIssue = {
  identifier: 'ENG-10',
  title: 'Blocked task',
  state: 'Todo',
  orchestratorState: 'idle',
  blockedBy: ['ENG-1', 'ENG-2'],
  blockedByDetails: [
    { identifier: 'ENG-1', state: 'In Progress' },
    { identifier: 'ENG-2', state: 'Done' },
  ],
};

describe('ListView', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: null });
  });

  // outbox #54 fast-follow: ListView previously had no "⟳ Syncing" marker
  // (accepted-minor D, final review) even though BoardView's DraggableCard
  // has carried it since Task 4 — same join-by-identifier against
  // snapshot.outboxSyncing.
  it('shows the syncing badge when the issue is in snapshot.outboxSyncing', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ outboxSyncing: ['ENG-10'] }) });
    render(
      <ListView
        issues={[baseIssue]}
        onSelect={vi.fn()}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
    );

    expect(screen.getByTestId('issue-row-syncing-badge')).toBeInTheDocument();
  });

  it('does not show the syncing badge when the issue is not in snapshot.outboxSyncing', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ outboxSyncing: ['OTHER-1'] }) });
    render(
      <ListView
        issues={[baseIssue]}
        onSelect={vi.fn()}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
    );

    expect(screen.queryByTestId('issue-row-syncing-badge')).not.toBeInTheDocument();
  });

  it('does not show the syncing badge when there is no snapshot at all', () => {
    render(
      <ListView
        issues={[baseIssue]}
        onSelect={vi.fn()}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
    );

    expect(screen.queryByTestId('issue-row-syncing-badge')).not.toBeInTheDocument();
  });

  it('shows a compact blocked count on issue rows', () => {
    render(
      <ListView
        issues={[baseIssue]}
        onSelect={vi.fn()}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
    );

    const badge = screen.getByText('Blocked 2');
    expect(badge).toBeInTheDocument();
    expect(badge).toHaveAttribute('title', 'Blocked by 2 issues');
  });

  it('falls back to blockedBy when blocker details are absent', () => {
    render(
      <ListView
        issues={[{ ...baseIssue, blockedBy: ['ENG-1'], blockedByDetails: [] }]}
        onSelect={vi.fn()}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
    );

    const badge = screen.getByText('Blocked 1');
    expect(badge).toBeInTheDocument();
    expect(badge).toHaveAttribute('title', 'Blocked by 1 issue');
  });
});
