import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import {
  makeAutomation,
  makeProfileDef,
  makeRunningRow,
} from '../../../../test/fixtures/snapshots';
import type {
  AutomationQueueRow,
  DependencyAuditRow,
  SSHHostInfo,
} from '../../../../types/schemas';
import { AutomationQueueDetailPanel } from '../AutomationQueueDetailPanel';

const queuedAt = '2026-05-20T10:00:00.000Z';

function queueRow(overrides: Partial<AutomationQueueRow> = {}): AutomationQueueRow {
  return {
    id: overrides.id ?? 'queue-1',
    automationId: overrides.automationId ?? 'cron-nightly',
    triggerType: overrides.triggerType ?? 'cron',
    identifier: overrides.identifier ?? 'ENG-42',
    title: overrides.title ?? 'Refresh stale backlog item',
    issueState: overrides.issueState ?? 'Backlog',
    profile: overrides.profile ?? 'release',
    backend: overrides.backend ?? 'codex',
    status: overrides.status ?? 'blocked',
    reason: overrides.reason ?? 'dependency_blocked',
    reasonDetail: overrides.reasonDetail ?? 'Waiting for blockers to finish',
    queuedAt: overrides.queuedAt ?? queuedAt,
    firedAt: overrides.firedAt ?? '2026-05-20T09:59:00.000Z',
    lastFiredAt: overrides.lastFiredAt,
    lastAttemptAt: overrides.lastAttemptAt,
    attemptCount: overrides.attemptCount ?? 2,
    cron: overrides.cron ?? '*/5 * * * *',
    timezone: overrides.timezone ?? 'Asia/Jerusalem',
    prUrl: overrides.prUrl,
    inputContext: overrides.inputContext,
    errorMessage: overrides.errorMessage,
    switchedToProfile: overrides.switchedToProfile,
    switchedToBackend: overrides.switchedToBackend,
    moveToState: overrides.moveToState,
  };
}

const dependencyAudit: DependencyAuditRow = {
  identifier: 'ENG-42',
  issueState: 'Backlog',
  status: 'blocked',
  sources: ['tracker_relation'],
  blockedBy: [{ identifier: 'ENG-10', state: 'In Progress' }],
  unresolvedBlockers: [{ identifier: 'ENG-10', state: 'In Progress' }],
  resolvedBlockers: [{ identifier: 'ENG-9', state: 'Done' }],
  wasBlocked: true,
  firstBlockedAt: '2026-05-20T09:00:00.000Z',
  lastAuditedAt: '2026-05-20T10:05:00.000Z',
  lastTransitionVersion: 7,
  lastTransitionReason: 'tracker blockers changed',
};

describe('AutomationQueueDetailPanel', () => {
  it('renders queue, trigger, dependency, config, profile, permissions, worker, and activity details', () => {
    render(
      <AutomationQueueDetailPanel
        row={queueRow()}
        automation={makeAutomation({
          id: 'cron-nightly',
          enabled: true,
          profile: 'release',
          instructions: 'Read the blog posts and refresh stale issues.',
          trigger: { type: 'cron', cron: '*/5 * * * *', timezone: 'Asia/Jerusalem' },
          filter: { matchMode: 'all', states: ['Backlog'], labelsAny: ['automation'], limit: 3 },
          policy: { autoResume: true, moveToState: 'Todo' },
        })}
        dependency={dependencyAudit}
        profileDef={makeProfileDef({
          command: 'codex --full-auto',
          backend: 'codex',
          enabled: true,
          allowedActions: ['comment', 'comment_pr', 'provide_input', 'move_state', 'create_issue'],
          createIssueState: 'Backlog',
        })}
        running={[
          makeRunningRow({
            identifier: 'ENG-42',
            workerHost: 'ssh-a.example.com',
            backend: 'codex',
            kind: 'automation',
          }),
        ]}
        maxConcurrentAgents={3}
        sshHosts={[
          { host: 'ssh-a.example.com', description: 'Build worker' } satisfies SSHHostInfo,
        ]}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByRole('dialog', { name: /automation cron-nightly/i })).toBeInTheDocument();
    expect(screen.getByText('ENG-42')).toBeInTheDocument();
    expect(screen.getByText('dependency blocked')).toBeInTheDocument();
    expect(screen.getByText('2 attempts')).toBeInTheDocument();
    expect(screen.getAllByText('Cron').length).toBeGreaterThan(0);
    expect(screen.getAllByText('*/5 * * * *').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Asia/Jerusalem').length).toBeGreaterThan(0);
    expect(screen.getByText('ENG-10')).toBeInTheDocument();
    expect(screen.getByText('ENG-9')).toBeInTheDocument();
    expect(screen.getByText('tracker_relation')).toBeInTheDocument();
    expect(screen.getByText('transition #7')).toBeInTheDocument();
    expect(screen.getAllByText('enabled').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Backlog').length).toBeGreaterThan(0);
    expect(screen.getByText('automation')).toBeInTheDocument();
    expect(screen.getByText('move to Todo')).toBeInTheDocument();
    expect(screen.getByText('codex --full-auto')).toBeInTheDocument();
    expect(screen.getByText('comment_pr')).toBeInTheDocument();
    expect(screen.getByText('create_issue')).toBeInTheDocument();
    expect(screen.getByText('capacity 1/3')).toBeInTheDocument();
    expect(screen.getAllByText('ssh-a.example.com').length).toBeGreaterThan(0);
    expect(screen.getByText('fired')).toBeInTheDocument();
    expect(screen.getAllByText('running').length).toBeGreaterThan(0);
  });

  it('closes from the left slide panel close button', () => {
    const onClose = vi.fn();
    render(<AutomationQueueDetailPanel row={queueRow()} onClose={onClose} />);

    fireEvent.click(screen.getByRole('button', { name: /close panel/i }));

    expect(onClose).toHaveBeenCalled();
  });

  it('keeps detail content in an internal scroll body for mobile panels', () => {
    render(<AutomationQueueDetailPanel row={queueRow()} onClose={vi.fn()} />);

    const body = screen.getByTestId('automation-queue-detail-body');
    expect(body.className).toContain('flex-1');
    expect(body.className).toContain('overflow-y-auto');
  });

  it('renders nothing without a selected queue row', () => {
    const { container } = render(<AutomationQueueDetailPanel row={null} onClose={vi.fn()} />);

    expect(container.firstChild).toBeNull();
  });
});
