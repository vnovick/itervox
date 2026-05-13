import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ListView } from '../ListView';
import type { TrackerIssue } from '../../../../types/schemas';

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
