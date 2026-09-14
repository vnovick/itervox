import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { useItervoxStore } from '../../../../store/itervoxStore';
import {
  makeHistoryRow,
  makeRetryRow,
  makeRunningRow,
  makeSnapshot,
} from '../../../../test/fixtures/snapshots';
import { LiveOpsStrip, liveOpsStripModel } from '../LiveOpsStrip';

describe('liveOpsStripModel', () => {
  it('reports offline when no snapshot is available', () => {
    expect(liveOpsStripModel(null).status).toBe('offline');
  });

  it('reports waiting when the daemon is live but no agents are running', () => {
    const model = liveOpsStripModel(makeSnapshot());

    expect(model.status).toBe('waiting');
    expect(model.capacityLabel).toBe('0/3');
  });

  it('summarizes queue, dependency, SSH, and automation activity', () => {
    const now = new Date();
    const today = now.toISOString();
    const yesterday = new Date(now.getTime() - 25 * 60 * 60_000).toISOString();
    const snapshot = makeSnapshot({
      running: [
        makeRunningRow({ identifier: 'DEMO-1', workerHost: 'ssh-a.example.com' }),
        makeRunningRow({ identifier: 'DEMO-2' }),
      ],
      retrying: [makeRetryRow()],
      paused: ['DEMO-PAUSED'],
      maxConcurrentAgents: 5,
      sshHosts: [{ host: 'ssh-a.example.com' }, { host: 'ssh-b.example.com' }],
      inputRequired: [
        {
          identifier: 'DEMO-INPUT',
          sessionId: 'input-1',
          state: 'input_required',
          context: 'Need approval.',
          queuedAt: today,
        },
      ],
      automationQueue: [
        {
          id: 'q-1',
          automationId: 'cron-a',
          triggerType: 'cron',
          identifier: 'DEMO-Q1',
          profile: 'default',
          status: 'queued',
          reason: 'ready',
          queuedAt: today,
          firedAt: today,
          attemptCount: 0,
        },
        {
          id: 'q-2',
          automationId: 'dep-a',
          triggerType: 'blockers_resolved',
          identifier: 'DEMO-Q2',
          profile: 'default',
          status: 'blocked',
          reason: 'dependency_blocked',
          queuedAt: today,
          firedAt: today,
          attemptCount: 1,
        },
      ],
      automationQueueBackpressure: {
        length: 2,
        maxLength: 10,
        saturated: false,
        pausedProducers: false,
        rejectedSinceBoot: 0,
      },
      dependencyAudit: [
        {
          identifier: 'DEMO-BLOCKED',
          issueState: 'Backlog',
          status: 'blocked',
          wasBlocked: true,
        },
        {
          identifier: 'DEMO-UNBLOCKED',
          issueState: 'Backlog',
          status: 'unblocked',
          wasBlocked: true,
          unblockedAt: today,
        },
      ],
      history: [
        makeHistoryRow({ identifier: 'DEMO-AUTO-1', finishedAt: today, automationId: 'cron-a' }),
        makeHistoryRow({
          identifier: 'DEMO-AUTO-OLD',
          finishedAt: yesterday,
          automationId: 'cron-a',
        }),
        makeHistoryRow({ identifier: 'DEMO-MANUAL', finishedAt: today, automationId: undefined }),
      ],
    });

    const model = liveOpsStripModel(snapshot, now.getTime());

    expect(model.status).toBe('live');
    expect(model.capacityLabel).toBe('2/5');
    expect(model.queueLabel).toBe('2/10');
    expect(model.blockedQueueCount).toBe(1);
    expect(model.dependencyBlockedCount).toBe(1);
    expect(model.recentlyUnblockedCount).toBe(1);
    expect(model.inputRequiredCount).toBe(1);
    expect(model.retryCount).toBe(1);
    expect(model.pausedCount).toBe(1);
    expect(model.sshLabel).toBe('2 hosts · 1 active');
    expect(model.automationsToday).toBe(1);
  });

  // gaps_11 G-11 — the self-reentry drop counter is omitempty on the wire:
  // absent (older daemon / never fired) must read as 0, present must surface.
  it('reads the self-reentry drop counter and defaults absent to zero', () => {
    expect(liveOpsStripModel(makeSnapshot()).selfReentryDrops).toBe(0);
    expect(
      liveOpsStripModel(makeSnapshot({ automationDropsSelfReentryTotal: 3 })).selfReentryDrops,
    ).toBe(3);
  });

  // Task 7 — the off-loop dependency refresher (Task 6) was previously
  // invisible to operators. These fields make it visible.
  it('surfaces in-flight dependency refreshes', () => {
    const model = liveOpsStripModel(makeSnapshot({ depsRefreshInFlight: 8 }));
    expect(model.depsRefreshingCount).toBe(8);
  });

  it('surfaces degraded dependency rows', () => {
    const model = liveOpsStripModel(makeSnapshot({ depsRefreshDegradedCount: 2 }));
    expect(model.depsDegradedCount).toBe(2);
  });

  // A states-only refresh batch (blockers_resolved scan with no row targets)
  // legitimately reports batch size 0 while the daemon is mid-refresh. Both
  // fields must default cleanly to 0 rather than throwing or going
  // undefined.
  it('defaults deps-refresh fields to zero when absent from the wire', () => {
    const model = liveOpsStripModel(makeSnapshot());
    expect(model.depsRefreshingCount).toBe(0);
    expect(model.depsDegradedCount).toBe(0);
  });

  // critical-path-ordering Task 4/5/6 — cycles and attention entries were
  // previously invisible to operators. Both fields are omitempty on the
  // wire; absent must default cleanly to zero.
  it('reads dependency cycle and attention counts, defaulting to zero when absent', () => {
    expect(liveOpsStripModel(makeSnapshot()).cycleCount).toBe(0);
    expect(liveOpsStripModel(makeSnapshot()).attentionCount).toBe(0);

    const model = liveOpsStripModel(
      makeSnapshot({
        dependencyCycles: [
          { members: ['ENG-1', 'ENG-2'], kind: 'tracker', detectedAt: '2026-05-25T12:00:00Z' },
        ],
        dependencyAttention: [
          {
            identifier: 'ENG-1',
            blockers: ['ENG-2'],
            blockedSince: '2026-05-25T09:00:00Z',
            kind: 'cycle',
          },
          {
            identifier: 'ENG-3',
            blockers: ['ENG-4'],
            blockedSince: '2026-05-20T09:00:00Z',
            kind: 'stale_blocker',
          },
        ],
      }),
    );
    expect(model.cycleCount).toBe(1);
    expect(model.attentionCount).toBe(2);
  });

  // outbox Task 4 — pending/degraded write-ahead-outbox counts. Both fields
  // are omitempty on the wire; absent must default cleanly to zero.
  it('reads outbox pending/degraded counts, defaulting to zero when absent', () => {
    const empty = liveOpsStripModel(makeSnapshot());
    expect(empty.outboxPendingCount).toBe(0);
    expect(empty.outboxDegradedCount).toBe(0);

    const model = liveOpsStripModel(
      makeSnapshot({
        outboxEntries: [
          {
            id: 'e1',
            kind: 'update_state',
            identifier: 'ENG-1',
            attempts: 1,
            enqueuedAt: '2026-05-25T12:00:00Z',
            nextAttemptAt: '2026-05-25T12:00:00Z',
          },
          {
            id: 'e2',
            kind: 'update_state',
            identifier: 'ENG-2',
            attempts: 6,
            degraded: true,
            enqueuedAt: '2026-05-25T12:00:00Z',
            nextAttemptAt: '2026-05-25T12:00:00Z',
          },
        ],
      }),
    );
    expect(model.outboxPendingCount).toBe(2);
    expect(model.outboxDegradedCount).toBe(1);
  });
});

describe('LiveOpsStrip', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: null });
  });

  it('renders live, waiting, and offline status labels', () => {
    const { rerender } = render(<LiveOpsStrip />);

    expect(screen.getByText('Offline')).toBeInTheDocument();

    useItervoxStore.setState({ snapshot: makeSnapshot() });
    rerender(<LiveOpsStrip />);

    expect(screen.getByText('Waiting')).toBeInTheDocument();

    useItervoxStore.setState({ snapshot: makeSnapshot({ running: [makeRunningRow()] }) });
    rerender(<LiveOpsStrip />);

    expect(screen.getByText('Live')).toBeInTheDocument();
  });

  it('renders the compact operational counters', () => {
    const now = new Date().toISOString();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        running: [makeRunningRow()],
        retrying: [makeRetryRow()],
        paused: ['DEMO-PAUSED'],
        automationQueue: [
          {
            id: 'q-1',
            automationId: 'cron-a',
            triggerType: 'cron',
            identifier: 'DEMO-Q1',
            profile: 'default',
            status: 'queued',
            reason: 'ready',
            queuedAt: now,
            firedAt: now,
            attemptCount: 0,
          },
        ],
        automationQueueBackpressure: {
          length: 1,
          maxLength: 3,
          saturated: false,
          pausedProducers: false,
          rejectedSinceBoot: 0,
        },
        inputRequired: [
          {
            identifier: 'DEMO-INPUT',
            sessionId: 'input-1',
            state: 'input_required',
            context: 'Need approval.',
            queuedAt: now,
          },
        ],
      }),
    });

    render(<LiveOpsStrip />);

    expect(screen.getByText('Capacity 1/3')).toBeInTheDocument();
    expect(screen.getByText('Queue 1/3')).toBeInTheDocument();
    expect(screen.getByText('Blocked 0')).toBeInTheDocument();
    expect(screen.getByText('Input 1')).toBeInTheDocument();
    expect(screen.getByText('Retry 1')).toBeInTheDocument();
    expect(screen.getByText('Paused 1')).toBeInTheDocument();
    expect(screen.getByText('Automations 0 today')).toBeInTheDocument();
  });

  it('renders a red queue-full alert when backpressure pauses producers', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        automationQueueBackpressure: {
          length: 100,
          maxLength: 100,
          saturated: true,
          pausedProducers: true,
          rejectedSinceBoot: 2,
        },
      }),
    });

    render(<LiveOpsStrip />);

    const alert = screen.getByRole('status', { name: /automation queue full/i });
    expect(alert).toHaveTextContent('Automation queue full 100/100');
    expect(alert).toHaveTextContent('new automation triggers paused');
  });

  it('keeps the queue-full alert first and wrapping on narrow screens', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        automationQueueBackpressure: {
          length: 100,
          maxLength: 100,
          saturated: true,
          pausedProducers: true,
          rejectedSinceBoot: 2,
        },
      }),
    });

    render(<LiveOpsStrip />);

    const strip = screen.getByTestId('live-ops-strip');
    const alert = screen.getByRole('status', { name: /automation queue full/i });
    expect(strip.textContent.indexOf('Automation queue full')).toBeLessThan(
      strip.textContent.indexOf('Waiting'),
    );
    expect(alert.className).not.toContain('truncate');
    expect(alert.className).toContain('whitespace-normal');
  });

  it('wraps all operational chips instead of clipping a horizontal lane', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        running: [makeRunningRow({ workerHost: 'ssh-a.example.com' })],
        sshHosts: [{ host: 'ssh-a.example.com' }],
        automationQueueBackpressure: {
          length: 100,
          maxLength: 100,
          saturated: true,
          pausedProducers: true,
          rejectedSinceBoot: 2,
        },
      }),
    });

    render(<LiveOpsStrip />);

    const chipRow = screen.getByTestId('live-ops-chip-row');
    expect(chipRow.className).toContain('flex-wrap');
    expect(chipRow.className).not.toContain('overflow-x-auto');
    expect(screen.getByText('Blocked 0')).toBeInTheDocument();
    expect(screen.getByText('Automations 0 today')).toBeInTheDocument();
  });

  // v0.2.0 audit P1-8 — dispatch treats unknown blocker status the same as
  // blocked, so the chip must surface the unresolved total (blocked + unknown)
  // and break out the unknown count parenthetically.
  it('counts unknown rows alongside blocked rows in the dependency chip', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        dependencyAudit: [
          { identifier: 'A', issueState: 'Backlog', status: 'blocked', wasBlocked: true },
          { identifier: 'B', issueState: 'Backlog', status: 'unknown', wasBlocked: true },
          { identifier: 'C', issueState: 'Backlog', status: 'unknown', wasBlocked: true },
        ],
      }),
    });
    render(<LiveOpsStrip />);
    expect(screen.getByText('Deps 3 unresolved (2 unknown)')).toBeInTheDocument();
  });

  // gaps_11 G-11 — the chip only appears once the guard has fired; healthy
  // daemons keep the strip compact.
  it('renders the self-reentry drops chip only when the counter is positive', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot() });
    const { rerender } = render(<LiveOpsStrip />);
    expect(screen.queryByText(/Self-reentry drops/)).not.toBeInTheDocument();

    useItervoxStore.setState({ snapshot: makeSnapshot({ automationDropsSelfReentryTotal: 3 }) });
    rerender(<LiveOpsStrip />);
    expect(screen.getByText('Self-reentry drops 3')).toBeInTheDocument();
  });

  it('falls back to the plain "Deps N blocked" label when no unknown rows exist', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        dependencyAudit: [
          { identifier: 'A', issueState: 'Backlog', status: 'blocked', wasBlocked: true },
        ],
      }),
    });
    render(<LiveOpsStrip />);
    expect(screen.getByText('Deps 1 blocked')).toBeInTheDocument();
  });

  // Task 7 — the off-loop dependency refresher (Task 6) was previously
  // invisible to operators: the deps chip never distinguished "working" from
  // "stuck". These render-level tests cover the new suffix.
  it('renders the refreshing suffix on the deps chip', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ depsRefreshInFlight: 8 }) });
    render(<LiveOpsStrip />);
    expect(screen.getByText(/refreshing 8/)).toBeInTheDocument();
  });

  it('renders the stale suffix when rows are degraded', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ depsRefreshDegradedCount: 2 }) });
    render(<LiveOpsStrip />);
    expect(screen.getByText(/2 stale/)).toBeInTheDocument();
  });

  it('marks the deps chip danger when rows are degraded', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ depsRefreshDegradedCount: 1 }) });
    render(<LiveOpsStrip />);
    const chip = screen.getByText(/1 stale/);
    expect(chip.className).toContain('text-theme-danger');
  });

  // The known "refreshing 0" trap: a states-only batch (blockers_resolved
  // scan with no row targets) can be in-flight with batch size 0. The chip
  // must render the plain base label with no "refreshing" suffix at all —
  // never "refreshing 0".
  it('does not render a "refreshing 0" suffix for a zero-size in-flight batch', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        depsRefreshInFlight: 0,
        dependencyAudit: [
          { identifier: 'A', issueState: 'Backlog', status: 'blocked', wasBlocked: true },
        ],
      }),
    });
    render(<LiveOpsStrip />);
    expect(screen.queryByText(/refreshing/)).not.toBeInTheDocument();
    expect(screen.getByText('Deps 1 blocked')).toBeInTheDocument();
  });

  // critical-path-ordering Task 4/5/6 — the cycles/attention tile stays
  // hidden on a healthy daemon (both counts zero) and appears once either
  // count is non-zero, mirroring the self-reentry-drops chip's convention.
  it('hides the dependency cycles/attention tile when both counts are zero', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot() });
    render(<LiveOpsStrip />);
    expect(screen.queryByText(/dependency cycle/)).not.toBeInTheDocument();
    expect(screen.queryByText(/need attention/)).not.toBeInTheDocument();
  });

  it('renders the dependency cycles/attention tile once cycles are present', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        dependencyCycles: [
          { members: ['ENG-1', 'ENG-2'], kind: 'tracker', detectedAt: '2026-05-25T12:00:00Z' },
        ],
      }),
    });
    render(<LiveOpsStrip />);
    const chip = screen.getByText('1 dependency cycle');
    expect(chip.className).toContain('text-theme-danger');
  });

  it('renders the dependency attention count with warning severity when there are no cycles', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        dependencyAttention: [
          {
            identifier: 'ENG-3',
            blockers: ['ENG-4'],
            blockedSince: '2026-05-20T09:00:00Z',
            kind: 'stale_blocker',
          },
        ],
      }),
    });
    render(<LiveOpsStrip />);
    const chip = screen.getByText('1 need attention');
    expect(chip.className).toContain('text-theme-warning');
  });

  it('combines cycle and attention counts into a single tile', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        dependencyCycles: [
          { members: ['ENG-1', 'ENG-2'], kind: 'tracker', detectedAt: '2026-05-25T12:00:00Z' },
        ],
        dependencyAttention: [
          {
            identifier: 'ENG-1',
            blockers: ['ENG-2'],
            blockedSince: '2026-05-25T09:00:00Z',
            kind: 'cycle',
          },
          {
            identifier: 'ENG-3',
            blockers: ['ENG-4'],
            blockedSince: '2026-05-20T09:00:00Z',
            kind: 'stale_blocker',
          },
        ],
      }),
    });
    render(<LiveOpsStrip />);
    expect(screen.getByText('1 dependency cycle · 2 need attention')).toBeInTheDocument();
  });

  // outbox Task 4 — LiveOps tile: hidden at zero, info-tone with no
  // degraded entries, danger-tone once any entry is degraded.
  it('hides the outbox tile when there are no pending entries', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot() });
    render(<LiveOpsStrip />);
    expect(screen.queryByText(/^Outbox /)).not.toBeInTheDocument();
  });

  it('renders the outbox tile with a non-danger tone when nothing is degraded', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        outboxEntries: [
          {
            id: 'e1',
            kind: 'update_state',
            identifier: 'ENG-1',
            attempts: 1,
            enqueuedAt: '2026-05-25T12:00:00Z',
            nextAttemptAt: '2026-05-25T12:00:00Z',
          },
        ],
      }),
    });
    render(<LiveOpsStrip />);
    const chip = screen.getByText('Outbox 1 pending');
    expect(chip.className).not.toContain('text-theme-danger');
  });

  it('renders the outbox tile with danger tone and the degraded suffix once any entry is degraded', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        outboxEntries: [
          {
            id: 'e1',
            kind: 'update_state',
            identifier: 'ENG-1',
            attempts: 6,
            degraded: true,
            enqueuedAt: '2026-05-25T12:00:00Z',
            nextAttemptAt: '2026-05-25T12:00:00Z',
          },
        ],
      }),
    });
    render(<LiveOpsStrip />);
    const chip = screen.getByText('Outbox 1 pending · 1 degraded');
    expect(chip.className).toContain('text-theme-danger');
  });
});
