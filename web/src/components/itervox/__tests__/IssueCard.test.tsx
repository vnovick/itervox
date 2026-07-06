import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import IssueCard from '../IssueCard';
import type { TrackerIssue } from '../../../types/schemas';

const baseIssue: TrackerIssue = {
  identifier: 'ABC-1',
  title: 'Fix the bug',
  state: 'In Progress',
  description: '',
  url: 'https://example.com/ABC-1',
  orchestratorState: 'running',
  turnCount: 3,
  tokens: 1000,
  elapsedMs: 90000,
  lastMessage: '',
  error: '',
  blockedBy: [],
  blockedByDetails: [],
};

describe('IssueCard', () => {
  it('renders identifier and title', () => {
    render(<IssueCard issue={baseIssue} onSelect={vi.fn()} />);
    expect(screen.getByText('ABC-1')).toBeInTheDocument();
    expect(screen.getByText('Fix the bug')).toBeInTheDocument();
  });

  it('renders elapsed time when elapsedMs > 0', () => {
    render(<IssueCard issue={baseIssue} onSelect={vi.fn()} />);
    expect(screen.getByText(/1m 30s/)).toBeInTheDocument();
  });

  it('does not render elapsed when elapsedMs is 0', () => {
    render(<IssueCard issue={{ ...baseIssue, elapsedMs: 0 }} onSelect={vi.fn()} />);
    expect(screen.queryByText(/⏱/)).not.toBeInTheDocument();
  });

  it('calls onSelect with identifier when clicked', async () => {
    const onSelect = vi.fn();
    render(<IssueCard issue={baseIssue} onSelect={onSelect} />);
    await userEvent.click(screen.getByText('Fix the bug'));
    expect(onSelect).toHaveBeenCalledWith('ABC-1');
  });

  it('renders URL as a link', () => {
    render(<IssueCard issue={baseIssue} onSelect={vi.fn()} />);
    const link = screen.getByRole('link');
    expect(link).toHaveAttribute('href', 'https://example.com/ABC-1');
  });

  it('shows a blocked badge when blockers exist', () => {
    render(
      <IssueCard
        issue={{
          ...baseIssue,
          blockedBy: ['ABC-0', 'ABC-2'],
          blockedByDetails: [
            { identifier: 'ABC-0', state: 'In Progress' },
            { identifier: 'ABC-2', state: 'Done' },
          ],
        }}
        onSelect={vi.fn()}
      />,
    );

    const badge = screen.getByTestId('issue-card-blocked-badge');
    expect(badge).toHaveTextContent('Blocked 2');
    expect(badge).toHaveAttribute('title', 'Blocked by 2 issues');
  });

  it('falls back to blockedBy when blocker details are absent', () => {
    render(
      <IssueCard
        issue={{
          ...baseIssue,
          blockedBy: ['ABC-0'],
          blockedByDetails: [],
        }}
        onSelect={vi.fn()}
      />,
    );

    const badge = screen.getByTestId('issue-card-blocked-badge');
    expect(badge).toHaveTextContent('Blocked 1');
    expect(badge).toHaveAttribute('title', 'Blocked by 1 issue');
  });

  it('renders identifier as plain text when no url', () => {
    render(<IssueCard issue={{ ...baseIssue, url: '' }} onSelect={vi.fn()} />);
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
    expect(screen.getByText('ABC-1')).toBeInTheDocument();
  });

  it('applies dragging styles when isDragging is true', () => {
    const { container } = render(<IssueCard issue={baseIssue} onSelect={vi.fn()} isDragging />);
    expect(container.firstChild).toHaveClass('rotate-1');
  });

  it('shows green status dot for running issue', () => {
    const { container } = render(<IssueCard issue={baseIssue} onSelect={vi.fn()} />);
    const dot = container.querySelector('.bg-theme-success');
    expect(dot).toBeInTheDocument();
  });

  it('shows warning status dot for paused issue', () => {
    const pausedIssue = { ...baseIssue, orchestratorState: 'paused' as const };
    const { container } = render(<IssueCard issue={pausedIssue} onSelect={vi.fn()} />);
    const dot = container.querySelector('.bg-theme-warning');
    expect(dot).toBeInTheDocument();
  });

  it('shows danger status dot for retrying issue', () => {
    const retryingIssue = { ...baseIssue, orchestratorState: 'retrying' as const };
    const { container } = render(<IssueCard issue={retryingIssue} onSelect={vi.fn()} />);
    const dot = container.querySelector('.bg-theme-danger');
    expect(dot).toBeInTheDocument();
  });

  it('shows orange status dot for input_required issue', () => {
    const inputIssue = { ...baseIssue, orchestratorState: 'input_required' as const };
    const { container } = render(<IssueCard issue={inputIssue} onSelect={vi.fn()} />);
    const dot = container.querySelector('.bg-orange-400');
    expect(dot).toBeInTheDocument();
  });

  it('shows orange status dot for pending_input_resume issue', () => {
    const pendingIssue = { ...baseIssue, orchestratorState: 'pending_input_resume' as const };
    const { container } = render(<IssueCard issue={pendingIssue} onSelect={vi.fn()} />);
    const dot = container.querySelector('.bg-orange-400');
    expect(dot).toBeInTheDocument();
  });

  it('shows transparent/muted dot for idle issue', () => {
    const idleIssue = { ...baseIssue, orchestratorState: 'idle' as const };
    const { container } = render(<IssueCard issue={idleIssue} onSelect={vi.fn()} />);
    // idle => bg-transparent (not active), so no success/warning/danger dot
    expect(container.querySelector('.bg-theme-success')).not.toBeInTheDocument();
    expect(container.querySelector('.bg-theme-warning')).not.toBeInTheDocument();
    expect(container.querySelector('.bg-transparent')).toBeInTheDocument();
  });

  it('shows Claude backend badge by default', () => {
    render(<IssueCard issue={baseIssue} onSelect={vi.fn()} />);
    expect(screen.getByText('Claude')).toBeInTheDocument();
  });

  it('shows Codex backend badge when defaultBackend is codex', () => {
    render(<IssueCard issue={baseIssue} onSelect={vi.fn()} defaultBackend="codex" />);
    expect(screen.getByText('Codex')).toBeInTheDocument();
  });

  it('shows Codex badge when runningBackend contains codex', () => {
    render(<IssueCard issue={baseIssue} onSelect={vi.fn()} runningBackend="codex-cli" />);
    expect(screen.getByText('Codex')).toBeInTheDocument();
  });

  it('shows Claude badge when profile backend is claude', () => {
    render(
      <IssueCard
        issue={{ ...baseIssue, agentProfile: 'myprofile' }}
        onSelect={vi.fn()}
        profileDefs={{ myprofile: { command: 'claude-code', prompt: 'test' } }}
      />,
    );
    expect(screen.getByText('Claude')).toBeInTheDocument();
  });

  it('shows Codex badge when profile backend field is codex', () => {
    render(
      <IssueCard
        issue={{ ...baseIssue, agentProfile: 'myprofile' }}
        onSelect={vi.fn()}
        profileDefs={{ myprofile: { command: 'codex', prompt: 'test', backend: 'codex' } }}
      />,
    );
    expect(screen.getByText('Codex')).toBeInTheDocument();
  });

  it('shows running state badge for running issue', () => {
    render(<IssueCard issue={baseIssue} onSelect={vi.fn()} />);
    expect(screen.getByText('running')).toBeInTheDocument();
  });

  it('shows paused state badge for paused issue', () => {
    const pausedIssue = { ...baseIssue, orchestratorState: 'paused' as const };
    render(<IssueCard issue={pausedIssue} onSelect={vi.fn()} />);
    expect(screen.getByText('paused')).toBeInTheDocument();
  });

  it('shows "Needs Input" badge for input_required issue', () => {
    const inputIssue = { ...baseIssue, orchestratorState: 'input_required' as const };
    render(<IssueCard issue={inputIssue} onSelect={vi.fn()} />);
    expect(screen.getByText('Needs Input')).toBeInTheDocument();
  });

  it('shows "Resuming" badge for pending_input_resume issue', () => {
    const pendingIssue = { ...baseIssue, orchestratorState: 'pending_input_resume' as const };
    render(<IssueCard issue={pendingIssue} onSelect={vi.fn()} />);
    expect(screen.getByText('Resuming')).toBeInTheDocument();
  });

  it('does not show state badge for idle issue', () => {
    const idleIssue = { ...baseIssue, orchestratorState: 'idle' as const };
    render(<IssueCard issue={idleIssue} onSelect={vi.fn()} />);
    expect(screen.queryByText('idle')).not.toBeInTheDocument();
    expect(screen.queryByText('Needs Input')).not.toBeInTheDocument();
  });

  it('shows profile selector dropdown when onDispatch is provided and issue is idle', () => {
    const idleIssue = { ...baseIssue, orchestratorState: 'idle' as const };
    render(
      <IssueCard
        issue={idleIssue}
        onSelect={vi.fn()}
        onDispatch={vi.fn()}
        availableProfiles={['fast', 'thorough']}
        onProfileChange={vi.fn()}
      />,
    );
    expect(screen.getByRole('combobox')).toBeInTheDocument();
  });

  it('does not show profile selector for running issue even with onDispatch', () => {
    render(
      <IssueCard
        issue={baseIssue}
        onSelect={vi.fn()}
        onDispatch={vi.fn()}
        availableProfiles={['fast']}
        onProfileChange={vi.fn()}
      />,
    );
    // Running is active, so isEditable is false => no dropdown
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
  });

  it('shows read-only profile badge for non-editable card with profiles', () => {
    render(
      <IssueCard
        issue={{ ...baseIssue, agentProfile: 'fast' }}
        onSelect={vi.fn()}
        availableProfiles={['fast', 'thorough']}
      />,
    );
    // No onDispatch => not editable, so shows read-only badge
    expect(screen.getByText('fast')).toBeInTheDocument();
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
  });

  it('shows dispatch button when onDispatch is provided', () => {
    const idleIssue = { ...baseIssue, orchestratorState: 'idle' as const };
    render(<IssueCard issue={idleIssue} onSelect={vi.fn()} onDispatch={vi.fn()} />);
    expect(screen.getByTitle('Send to queue')).toBeInTheDocument();
  });

  it('calls onDispatch with identifier when dispatch button clicked', async () => {
    const onDispatch = vi.fn();
    const idleIssue = { ...baseIssue, orchestratorState: 'idle' as const };
    render(<IssueCard issue={idleIssue} onSelect={vi.fn()} onDispatch={onDispatch} />);
    await userEvent.click(screen.getByTitle('Send to queue'));
    expect(onDispatch).toHaveBeenCalledWith('ABC-1');
  });

  it('does not show dispatch button when onDispatch is not provided', () => {
    render(<IssueCard issue={baseIssue} onSelect={vi.fn()} />);
    expect(screen.queryByTitle('Send to queue')).not.toBeInTheDocument();
  });

  it('does not render elapsed when elapsedMs is undefined', () => {
    render(<IssueCard issue={{ ...baseIssue, elapsedMs: undefined }} onSelect={vi.fn()} />);
    expect(screen.queryByText(/\d+[ms]/)).not.toBeInTheDocument();
  });

  it('does not apply dragging styles when isDragging is false', () => {
    const { container } = render(
      <IssueCard issue={baseIssue} onSelect={vi.fn()} isDragging={false} />,
    );
    expect(container.firstChild).not.toHaveClass('rotate-1');
  });

  describe('T-6: review-comment badge', () => {
    it('renders the badge when latest run is reviewer with commentCount > 0', () => {
      render(
        <IssueCard issue={baseIssue} onSelect={vi.fn()} runningKind="reviewer" commentCount={3} />,
      );
      const badge = screen.getByTestId('issue-card-review-badge');
      expect(badge).toBeInTheDocument();
      expect(badge.textContent).toContain('3');
      expect(badge.textContent.toLowerCase()).toContain('review');
    });

    it('uses the singular "review" form when commentCount equals 1', () => {
      render(
        <IssueCard issue={baseIssue} onSelect={vi.fn()} runningKind="reviewer" commentCount={1} />,
      );
      const badge = screen.getByTestId('issue-card-review-badge');
      expect(badge.textContent).toMatch(/1 review\b/);
    });

    it('hides the badge for non-reviewer runs', () => {
      render(
        <IssueCard issue={baseIssue} onSelect={vi.fn()} runningKind="worker" commentCount={5} />,
      );
      expect(screen.queryByTestId('issue-card-review-badge')).not.toBeInTheDocument();
    });

    it('hides the badge when commentCount is zero', () => {
      render(
        <IssueCard issue={baseIssue} onSelect={vi.fn()} runningKind="reviewer" commentCount={0} />,
      );
      expect(screen.queryByTestId('issue-card-review-badge')).not.toBeInTheDocument();
    });

    it('opens the issue detail slide when the badge is clicked', async () => {
      const onSelect = vi.fn();
      render(
        <IssueCard issue={baseIssue} onSelect={onSelect} runningKind="reviewer" commentCount={2} />,
      );
      await userEvent.click(screen.getByTestId('issue-card-review-badge'));
      expect(onSelect).toHaveBeenCalledWith('ABC-1');
    });
  });

  describe('G: retry-in-flight badge', () => {
    const retryingIssue: TrackerIssue = {
      ...baseIssue,
      orchestratorState: 'retrying',
    };

    it('renders "↻ retry N/M" with the configured max_retries denominator', () => {
      render(
        <IssueCard issue={retryingIssue} onSelect={vi.fn()} retryAttempt={2} maxRetries={5} />,
      );
      expect(screen.getByTestId('issue-card-retry-badge')).toHaveTextContent('↻ retry 2/5');
    });

    it('omits the denominator when max_retries=0 (unlimited)', () => {
      render(
        <IssueCard issue={retryingIssue} onSelect={vi.fn()} retryAttempt={3} maxRetries={0} />,
      );
      const badge = screen.getByTestId('issue-card-retry-badge');
      expect(badge).toHaveTextContent('↻ retry 3');
      expect(badge).not.toHaveTextContent('/');
      expect(badge).toHaveAttribute('title', expect.stringContaining('unlimited'));
    });

    it('does not render the badge when the issue is not retrying', () => {
      render(<IssueCard issue={baseIssue} onSelect={vi.fn()} retryAttempt={2} maxRetries={5} />);
      expect(screen.queryByTestId('issue-card-retry-badge')).not.toBeInTheDocument();
    });

    it('does not render the badge when retryAttempt is undefined', () => {
      render(<IssueCard issue={retryingIssue} onSelect={vi.fn()} maxRetries={5} />);
      expect(screen.queryByTestId('issue-card-retry-badge')).not.toBeInTheDocument();
    });
  });
});
