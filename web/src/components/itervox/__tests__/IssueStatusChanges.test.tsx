import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { IssueStatusChanges } from '../IssueStatusChanges';

const changes = [
  {
    fromState: 'Todo',
    toState: 'In Progress',
    source: 'worker_lifecycle',
    profileName: 'default',
    backend: 'codex',
    workerHost: 'ssh-build-1',
    at: '2026-05-20T09:31:00Z',
  },
  {
    fromState: 'In Progress',
    toState: 'In Review',
    source: 'automation',
    automationId: 'dispatch-reviewer-on-pr',
    triggerType: 'pr_opened',
    at: '2026-05-20T10:04:00Z',
  },
  {
    fromState: 'In Review',
    toState: 'Done',
    source: 'tracker_observed',
    at: '2026-05-20T11:15:00Z',
  },
];

describe('IssueStatusChanges', () => {
  it('renders status transitions with source and automation metadata', () => {
    render(<IssueStatusChanges changes={changes} />);

    expect(screen.getByRole('heading', { name: /status changes/i })).toBeInTheDocument();
    expect(screen.getByText('Todo')).toBeInTheDocument();
    expect(screen.getAllByText('In Progress')).toHaveLength(2);
    expect(screen.getAllByText('In Review')).toHaveLength(2);
    expect(screen.getByText('Done')).toBeInTheDocument();
    expect(screen.getByText('worker_lifecycle')).toBeInTheDocument();
    expect(screen.getByText('automation')).toBeInTheDocument();
    expect(screen.getByText('dispatch-reviewer-on-pr')).toBeInTheDocument();
    expect(screen.getByText('default')).toBeInTheDocument();
    expect(screen.getByText('codex')).toBeInTheDocument();
    expect(screen.getByText('ssh-build-1')).toBeInTheDocument();
  });

  it('uses wrapping vertical rows without horizontal overflow', () => {
    const { container } = render(<IssueStatusChanges changes={changes} />);
    const section = container.firstElementChild;

    expect(section?.className).not.toContain('overflow-x');
    expect(section?.querySelector('.flex-wrap')).not.toBeNull();
  });

  it('keeps long state names wrapping inside the timeline', () => {
    const { container } = render(
      <IssueStatusChanges
        changes={[
          {
            fromState: 'Extremely Long Intake State Name That Should Wrap',
            toState: 'Another Very Long Review State Name That Should Wrap',
            source: 'dashboard',
            workerHost: 'ssh-worker-with-a-very-long-name.example.com',
            at: '2026-05-20T09:31:00Z',
          },
        ]}
      />,
    );

    expect(container.querySelectorAll('.break-words').length).toBeGreaterThanOrEqual(2);
    expect(container.querySelector('.max-w-full')).not.toBeNull();
    expect(container.firstElementChild?.className).not.toContain('overflow-x');
  });

  it('renders nothing without changes', () => {
    const { container } = render(<IssueStatusChanges changes={[]} />);
    expect(container.firstChild).toBeNull();
  });
});
