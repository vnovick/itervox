import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type {
  AutomationQueueBackpressure,
  AutomationQueueRow,
  DependencyAuditRow,
} from '../../../../types/schemas';
import { AutomationQueueList, sortAutomationQueueRows } from '../AutomationQueueList';

const now = '2026-05-20T10:00:00.000Z';

function row(overrides: Partial<AutomationQueueRow>): AutomationQueueRow {
  return {
    id: overrides.id ?? `${overrides.automationId ?? 'auto'}-${overrides.identifier ?? 'ISSUE-1'}`,
    automationId: overrides.automationId ?? 'cron-nightly',
    triggerType: overrides.triggerType ?? 'cron',
    identifier: overrides.identifier ?? 'ISSUE-1',
    title: overrides.title ?? 'Queue item title',
    issueState: overrides.issueState ?? 'Backlog',
    profile: overrides.profile ?? 'default',
    backend: overrides.backend,
    status: overrides.status ?? 'queued',
    reason: overrides.reason ?? 'ready',
    reasonDetail: overrides.reasonDetail,
    queuedAt: overrides.queuedAt ?? now,
    firedAt: overrides.firedAt ?? now,
    lastFiredAt: overrides.lastFiredAt,
    lastAttemptAt: overrides.lastAttemptAt,
    attemptCount: overrides.attemptCount ?? 0,
    cron: overrides.cron,
    timezone: overrides.timezone,
    prUrl: overrides.prUrl,
    inputContext: overrides.inputContext,
    errorMessage: overrides.errorMessage,
    switchedToProfile: overrides.switchedToProfile,
    switchedToBackend: overrides.switchedToBackend,
    moveToState: overrides.moveToState,
  };
}

const dependencyAudit: DependencyAuditRow[] = [
  {
    identifier: 'ENG-DEP',
    issueState: 'Backlog',
    status: 'blocked',
    wasBlocked: true,
    unresolvedBlockers: [
      { identifier: 'ENG-BLOCKER-1', state: 'In Progress' },
      { identifier: 'ENG-BLOCKER-2', state: 'Todo' },
    ],
  },
];

describe('AutomationQueueList', () => {
  it('renders queued, blocked, dependency, and blockers-resolved entries', () => {
    render(
      <AutomationQueueList
        queue={[
          row({ automationId: 'cron-nightly', triggerType: 'cron', identifier: 'ENG-CRON' }),
          row({
            automationId: 'pr-helper',
            triggerType: 'pr_opened',
            identifier: 'ENG-PR',
            status: 'blocked',
            reason: 'no_slots',
            prUrl: 'https://example.com/pr/1',
          }),
          row({
            automationId: 'deps-helper',
            triggerType: 'issue_entered_state',
            identifier: 'ENG-DEP',
            status: 'blocked',
            reason: 'dependency_blocked',
          }),
          row({
            automationId: 'unblock-manager',
            triggerType: 'blockers_resolved',
            identifier: 'ENG-READY',
            reason: 'ready',
            moveToState: 'Todo',
          }),
        ]}
        dependencyAudit={dependencyAudit}
        onSelectIssue={vi.fn()}
        onSelectQueue={vi.fn()}
      />,
    );

    expect(screen.getByText('cron-nightly')).toBeInTheDocument();
    expect(screen.getByText('pr_opened')).toBeInTheDocument();
    expect(screen.getByText('ENG-BLOCKER-1')).toBeInTheDocument();
    expect(screen.getByText('ENG-BLOCKER-2')).toBeInTheDocument();
    expect(screen.getByText('blockers_resolved')).toBeInTheDocument();
    expect(screen.getByText('move to Todo')).toBeInTheDocument();
  });

  it('shows no-slot, per-state-limit, SSH/backend, and attempt metadata', () => {
    render(
      <AutomationQueueList
        queue={[
          row({
            automationId: 'capacity-helper',
            identifier: 'ENG-NO-SLOT',
            reason: 'no_slots',
            backend: 'codex',
            attemptCount: 2,
          }),
          row({
            automationId: 'state-limit-helper',
            identifier: 'ENG-LIMIT',
            reason: 'per_state_limit',
            reasonDetail: 'Backlog limit reached',
            backend: 'claude',
          }),
        ]}
        dependencyAudit={[]}
        onSelectIssue={vi.fn()}
        onSelectQueue={vi.fn()}
      />,
    );

    expect(screen.getByText('no slots')).toBeInTheDocument();
    expect(screen.getByText('per-state limit')).toBeInTheDocument();
    expect(screen.getByText('codex')).toBeInTheDocument();
    expect(screen.getByText('2 attempts')).toBeInTheDocument();
    expect(screen.getByText('Backlog limit reached')).toBeInTheDocument();
  });

  it('distinguishes an empty queue from an empty filtered result', () => {
    const { rerender } = render(
      <AutomationQueueList
        queue={[]}
        dependencyAudit={[]}
        onSelectIssue={vi.fn()}
        onSelectQueue={vi.fn()}
      />,
    );

    expect(screen.getByText('No automation queue items')).toBeInTheDocument();

    rerender(
      <AutomationQueueList
        queue={[row({ automationId: 'cron-nightly', identifier: 'ENG-CRON' })]}
        dependencyAudit={[]}
        onSelectIssue={vi.fn()}
        onSelectQueue={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByRole('searchbox', { name: /search automation queue/i }), {
      target: { value: 'missing' },
    });

    expect(screen.getByText('No matching automation queue items')).toBeInTheDocument();
  });

  it('filters locally by automation, trigger, issue, profile, backend, reason, and dependency identifier', () => {
    render(
      <AutomationQueueList
        queue={[
          row({
            automationId: 'cron-nightly',
            triggerType: 'cron',
            identifier: 'ENG-CRON',
            title: 'Nightly cleanup',
            profile: 'release',
            backend: 'claude',
            reason: 'ready',
          }),
          row({
            automationId: 'deps-helper',
            triggerType: 'issue_entered_state',
            identifier: 'ENG-DEP',
            title: 'Blocked by another issue',
            profile: 'pm',
            backend: 'codex',
            status: 'blocked',
            reason: 'dependency_blocked',
          }),
        ]}
        dependencyAudit={dependencyAudit}
        onSelectIssue={vi.fn()}
        onSelectQueue={vi.fn()}
      />,
    );

    const search = screen.getByRole('searchbox', { name: /search automation queue/i });
    fireEvent.change(search, { target: { value: 'ENG-BLOCKER-2' } });

    expect(screen.getByText('deps-helper')).toBeInTheDocument();
    expect(screen.queryByText('cron-nightly')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /clear queue search/i }));
    expect(screen.getByText('cron-nightly')).toBeInTheDocument();
    expect(screen.getByText('deps-helper')).toBeInTheDocument();
  });

  it('renders queue saturation alert and invokes callbacks', () => {
    const onSelectIssue = vi.fn();
    const onSelectQueue = vi.fn();
    const backpressure: AutomationQueueBackpressure = {
      length: 100,
      maxLength: 100,
      saturated: true,
      pausedProducers: true,
      rejectedSinceBoot: 4,
    };

    render(
      <AutomationQueueList
        queue={[row({ id: 'queue-1', automationId: 'cron-nightly', identifier: 'ENG-CRON' })]}
        dependencyAudit={[]}
        backpressure={backpressure}
        onSelectIssue={onSelectIssue}
        onSelectQueue={onSelectQueue}
      />,
    );

    expect(screen.getByRole('alert')).toHaveTextContent('Automation intake paused');

    fireEvent.click(screen.getByRole('button', { name: /open ENG-CRON details/i }));
    expect(onSelectQueue).toHaveBeenCalledWith('queue-1');

    fireEvent.click(screen.getByRole('button', { name: /open issue ENG-CRON/i }));
    expect(onSelectIssue).toHaveBeenCalledWith('ENG-CRON');
  });

  it('keeps the dense queue table at desktop widths to avoid tablet clipping', () => {
    render(
      <AutomationQueueList
        queue={[row({ id: 'queue-1', automationId: 'cron-nightly', identifier: 'ENG-CRON' })]}
        dependencyAudit={[]}
        onSelectIssue={vi.fn()}
        onSelectQueue={vi.fn()}
      />,
    );

    const header = screen.getByText('Automation').parentElement;
    const item = screen.getByTestId('automation-queue-card-ENG-CRON');
    expect(header?.className).toContain('lg:grid');
    expect(header?.className).not.toContain('md:grid');
    expect(item.className).toContain('lg:grid-cols-');
    expect(item.className).not.toContain('md:grid-cols-');
  });
});

describe('sortAutomationQueueRows', () => {
  it('orders blocked, ready blockers-resolved, queued oldest first, then dispatching', () => {
    const rows = [
      row({
        id: 'dispatching',
        automationId: 'dispatching',
        status: 'dispatching',
        queuedAt: '2026-05-20T10:04:00.000Z',
      }),
      row({ id: 'queued-new', automationId: 'queued-new', queuedAt: '2026-05-20T10:03:00.000Z' }),
      row({
        id: 'ready-unblocked',
        automationId: 'ready-unblocked',
        triggerType: 'blockers_resolved',
        reason: 'ready',
        queuedAt: '2026-05-20T10:02:00.000Z',
      }),
      row({
        id: 'blocked',
        automationId: 'blocked',
        status: 'blocked',
        queuedAt: '2026-05-20T10:01:00.000Z',
      }),
      row({ id: 'queued-old', automationId: 'queued-old', queuedAt: '2026-05-20T10:00:00.000Z' }),
    ];

    expect(sortAutomationQueueRows(rows).map((item) => item.id)).toEqual([
      'blocked',
      'ready-unblocked',
      'queued-old',
      'queued-new',
      'dispatching',
    ]);
  });

  it('renders mobile card affordances in the same content tree', () => {
    render(
      <AutomationQueueList
        queue={[row({ automationId: 'mobile-card', identifier: 'ENG-MOBILE' })]}
        dependencyAudit={[]}
        onSelectIssue={vi.fn()}
        onSelectQueue={vi.fn()}
      />,
    );

    const card = screen.getByTestId('automation-queue-card-ENG-MOBILE');
    expect(within(card).getByText('mobile-card')).toBeInTheDocument();
    expect(
      within(card).getByRole('button', { name: /open ENG-MOBILE details/i }),
    ).toBeInTheDocument();
  });

  it('keeps mobile card detail targets at least 36px tall', () => {
    render(
      <AutomationQueueList
        queue={[row({ automationId: 'touch-card', identifier: 'ENG-TOUCH' })]}
        dependencyAudit={[]}
        onSelectIssue={vi.fn()}
        onSelectQueue={vi.fn()}
      />,
    );

    const card = screen.getByTestId('automation-queue-card-ENG-TOUCH');
    expect(
      within(card).getByRole('button', { name: /open ENG-TOUCH details/i }).className,
    ).toContain('min-h-9');
  });
});
