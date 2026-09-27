import { act, render as rtlRender, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Profiler, type ReactElement } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useItervoxStore } from '../../../../store/itervoxStore';
import {
  makeHistoryRow,
  makeRetryRow,
  makeRunningRow,
  makeSnapshot,
} from '../../../../test/fixtures/snapshots';
import { LiveOpsStrip, liveOpsStripModel } from '../LiveOpsStrip';

// CORE-086 — linked chips render router <Link>s, so every render needs a router.
const render = (ui: ReactElement) => rtlRender(ui, { wrapper: MemoryRouter });

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

    // CORE-076 — running>0 is 'active'; 'Live' is the SSE connection only.
    expect(model.status).toBe('active');
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

  // Task 9 — trackerRateLimitedUntil is the latest future rateLimitedUntil
  // across outbox entries, computed against the injectable `now` so the
  // derivation stays pure (no Date.now() inside the model).
  it('returns the latest future rateLimitedUntil across outbox entries', () => {
    const now = new Date('2026-05-25T12:00:00Z');
    const earlierFuture = '2026-05-25T12:05:00Z';
    const laterFuture = '2026-05-25T12:10:00Z';
    const past = '2026-05-25T11:00:00Z';

    const model = liveOpsStripModel(
      makeSnapshot({
        outboxEntries: [
          {
            id: 'e1',
            kind: 'update_state',
            identifier: 'ENG-1',
            attempts: 1,
            enqueuedAt: '2026-05-25T11:00:00Z',
            nextAttemptAt: '2026-05-25T12:05:00Z',
            rateLimitedUntil: earlierFuture,
          },
          {
            id: 'e2',
            kind: 'update_state',
            identifier: 'ENG-2',
            attempts: 1,
            enqueuedAt: '2026-05-25T11:00:00Z',
            nextAttemptAt: '2026-05-25T12:10:00Z',
            rateLimitedUntil: laterFuture,
          },
          {
            id: 'e3',
            kind: 'update_state',
            identifier: 'ENG-3',
            attempts: 1,
            enqueuedAt: '2026-05-25T10:00:00Z',
            nextAttemptAt: '2026-05-25T11:00:00Z',
            rateLimitedUntil: past,
          },
        ],
      }),
      now.getTime(),
    );

    expect(model.trackerRateLimitedUntil).toBe(laterFuture);
  });

  it('returns null when every rateLimitedUntil is in the past or absent', () => {
    const now = new Date('2026-05-25T12:00:00Z');

    const model = liveOpsStripModel(
      makeSnapshot({
        outboxEntries: [
          {
            id: 'e1',
            kind: 'update_state',
            identifier: 'ENG-1',
            attempts: 1,
            enqueuedAt: '2026-05-25T10:00:00Z',
            nextAttemptAt: '2026-05-25T11:00:00Z',
            rateLimitedUntil: '2026-05-25T11:00:00Z',
          },
          {
            id: 'e2',
            kind: 'update_state',
            identifier: 'ENG-2',
            attempts: 1,
            enqueuedAt: '2026-05-25T10:00:00Z',
            nextAttemptAt: '2026-05-25T10:00:00Z',
          },
        ],
      }),
      now.getTime(),
    );

    expect(model.trackerRateLimitedUntil).toBeNull();
  });

  it('returns null for a null snapshot', () => {
    expect(liveOpsStripModel(null).trackerRateLimitedUntil).toBeNull();
  });
});

describe('LiveOpsStrip', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: null });
  });

  it('renders active, waiting, and offline status labels', () => {
    const { rerender } = render(<LiveOpsStrip />);

    expect(screen.getByText('Offline')).toBeInTheDocument();

    useItervoxStore.setState({ snapshot: makeSnapshot() });
    rerender(<LiveOpsStrip />);

    expect(screen.getByText('Waiting')).toBeInTheDocument();

    useItervoxStore.setState({ snapshot: makeSnapshot({ running: [makeRunningRow()] }) });
    rerender(<LiveOpsStrip />);

    expect(screen.getByText('Active')).toBeInTheDocument();
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
    expect(screen.getByText('Needs input 1')).toBeInTheDocument();
    expect(screen.getByText('Retrying 1')).toBeInTheDocument();
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
    expect(chip.className).toContain('text-theme-warning-text');
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

// CORE-055 — agent backend health chips. Labelled by backend so they are
// never read as the tracker's own "Tracker rate limited until" chip.
function renderWithQuery(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

describe('LiveOpsStrip limited backend chip (CORE-055)', () => {
  const hhmm = (iso: string) =>
    new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  // The backend chip shows the date when the reset is not today (BH-M3-9).
  const clock = (iso: string) =>
    new Date(iso).toDateString() === new Date().toDateString()
      ? hhmm(iso)
      : new Date(iso).toLocaleString([], {
          month: 'short',
          day: 'numeric',
          hour: '2-digit',
          minute: '2-digit',
        });

  beforeEach(() => {
    useItervoxStore.setState({ snapshot: null });
  });

  // BH-M3-9: a reset that is not today shows its date.
  it('limited backend with a reset on another day shows the date', () => {
    const reset = new Date(Date.now() + 3 * 24 * 60 * 60_000).toISOString();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        backendHealth: [
          {
            backend: 'claude',
            status: 'limited',
            limitedUntil: reset,
            retryAt: reset,
            heldIssues: 0,
            reroutedIssues: 0,
          },
        ],
      }),
    });
    renderWithQuery(<LiveOpsStrip />);
    const expected = new Date(reset).toLocaleString([], {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
    expect(screen.getByText(`Claude limited until ${expected}`)).toBeInTheDocument();
  });

  // M3-close V1: the operator can clear a breaker from the chip.
  it('clears a limited backend breaker after confirmation', async () => {
    const reset = new Date(Date.now() + 60 * 60_000).toISOString();
    const fetchMock = vi.fn(() =>
      Promise.resolve(new Response(JSON.stringify({ queued: true }), { status: 202 })),
    );
    global.fetch = fetchMock as unknown as typeof fetch;
    useItervoxStore.setState({
      refreshSnapshot: vi.fn().mockResolvedValue(undefined),
      snapshot: makeSnapshot({
        backendHealth: [
          {
            backend: 'codex',
            host: 'build-1',
            status: 'limited',
            limitedUntil: reset,
            retryAt: reset,
            heldIssues: 0,
            reroutedIssues: 0,
          },
        ],
      }),
    });
    const user = userEvent.setup();
    renderWithQuery(<LiveOpsStrip />);
    await user.click(screen.getByRole('button', { name: 'Clear Codex@build-1' }));
    await user.click(screen.getByRole('button', { name: 'Clear breaker?' }));
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        expect.stringContaining('/api/v1/backend-health/clear'),
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ backend: 'codex', host: 'build-1' }),
        }),
      );
    });
  });

  it('limited backend: names the backend and its limitedUntil time, distinct from the tracker chip', () => {
    const reset = new Date(Date.now() + 2 * 60 * 60_000).toISOString();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        backendHealth: [
          {
            backend: 'claude',
            status: 'limited',
            kind: 'quota',
            limitType: 'five_hour',
            limitedUntil: reset,
            retryAt: reset,
            heldIssues: 2,
            reroutedIssues: 1,
          },
          {
            backend: 'codex',
            status: 'healthy',
            limitedUntil: null,
            heldIssues: 0,
            reroutedIssues: 0,
          },
        ],
        autoSwitches: [{ identifier: 'ENG-3', source: 'backend_fallback', toBackend: 'codex' }],
        outboxEntries: [
          {
            id: 'e1',
            kind: 'comment',
            identifier: 'ENG-9',
            attempts: 1,
            rateLimitedUntil: reset,
            enqueuedAt: reset,
            nextAttemptAt: reset,
          },
        ],
      }),
    });
    renderWithQuery(<LiveOpsStrip />);
    const chip = screen.getByText(`Claude limited until ${clock(reset)} · 2 held · 1 rerouted`);
    expect(chip).toBeInTheDocument();
    expect(screen.getByText(`Tracker rate limited until ${hhmm(reset)}`)).toBeInTheDocument();
    expect(screen.queryByText(/^Codex/)).not.toBeInTheDocument();
    expect(screen.getByText('Auto-switched 1')).toBeInTheDocument();
  });

  it('limited backend with an unknown reset says so instead of inventing a time', () => {
    const retry = new Date(Date.now() + 15 * 60_000).toISOString();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        backendHealth: [
          {
            backend: 'codex',
            host: 'build-1',
            status: 'limited',
            kind: 'throttle',
            limitedUntil: null,
            retryAt: retry,
            heldIssues: 0,
            reroutedIssues: 0,
          },
          {
            backend: 'claude',
            status: 'probing',
            limitedUntil: null,
            probeIssue: 'ENG-7',
            heldIssues: 1,
            reroutedIssues: 0,
          },
        ],
      }),
    });
    renderWithQuery(<LiveOpsStrip />);
    expect(
      screen.getByText(`Codex@build-1 limited (reset unknown, retry ${clock(retry)})`),
    ).toBeInTheDocument();
    expect(screen.getByText('Claude probing (ENG-7) · 1 held')).toBeInTheDocument();
  });

  it('shows no backend chip while every backend is healthy or the daemon predates the field', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        backendHealth: [
          {
            backend: 'claude',
            status: 'healthy',
            limitedUntil: null,
            heldIssues: 0,
            reroutedIssues: 0,
          },
        ],
      }),
    });
    const { unmount } = renderWithQuery(<LiveOpsStrip />);
    expect(screen.queryByText(/Claude/)).not.toBeInTheDocument();
    unmount();
    useItervoxStore.setState({ snapshot: makeSnapshot() });
    renderWithQuery(<LiveOpsStrip />);
    expect(screen.queryByText(/limited|probing/)).not.toBeInTheDocument();
  });
});

// CORE-074 — LiveOpsStrip selects the branches its model reads (useShallow),
// so a push whose only change is the per-build generatedAt stamp does not
// re-render it; its clock comes from a coarse ticker instead of the push.
describe('LiveOpsStrip (CORE-074)', () => {
  it('does not re-render on a timestamp-only push', () => {
    const snap = makeSnapshot({
      running: [makeRunningRow({ identifier: 'DEMO-1' })],
      retrying: [makeRetryRow()],
      paused: ['DEMO-P'],
    });
    useItervoxStore.getState().setSnapshot(snap);
    let commits = 0;
    renderWithQuery(
      <Profiler
        id="strip"
        onRender={() => {
          commits += 1;
        }}
      >
        <LiveOpsStrip />
      </Profiler>,
    );
    const afterMount = commits;
    act(() => {
      const next = structuredClone(snap);
      next.generatedAt = '2026-09-27T12:00:00Z';
      useItervoxStore.getState().setSnapshot(next);
    });
    expect(useItervoxStore.getState().snapshot?.generatedAt).toBe('2026-09-27T12:00:00Z');
    expect(commits).toBe(afterMount);

    // A real change still re-renders.
    act(() => {
      const next = structuredClone(snap);
      next.paused = ['DEMO-P', 'DEMO-Q'];
      useItervoxStore.getState().setSnapshot(next);
    });
    expect(commits).toBeGreaterThan(afterMount);
    expect(screen.getByText('Paused 2')).toBeInTheDocument();
  });

  it('rate-limited chip disappears when the ticker crosses rateLimitedUntil without a new snapshot', () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date('2026-09-27T10:00:00Z'));
      const until = '2026-09-27T10:00:45Z';
      useItervoxStore.getState().setSnapshot(
        makeSnapshot({
          outboxEntries: [
            {
              id: 'rl',
              kind: 'comment',
              identifier: 'ENG-9',
              attempts: 1,
              rateLimitedUntil: until,
              enqueuedAt: '2026-09-27T09:59:00Z',
              nextAttemptAt: until,
            },
          ],
        }),
      );
      renderWithQuery(<LiveOpsStrip />);
      expect(screen.getByText(/^Tracker rate limited until/)).toBeInTheDocument();
      act(() => {
        vi.advanceTimersByTime(61_000);
      });
      expect(screen.queryByText(/^Tracker rate limited until/)).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });
});

// CORE-084 — the tracker API budget (snapshot.rateLimits) chip. Distinct from
// the outbox-derived "Tracker rate limited until HH:MM" chip and from the
// CORE-055 agent backend-health chips.
describe('LiveOpsStrip tracker API budget (CORE-084)', () => {
  const hhmm = (iso: string) =>
    new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: null });
  });

  it('shows rate limit chip with warning under 10%', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        rateLimits: { requestsLimit: 1500, requestsRemaining: 120 },
      }),
    });
    renderWithQuery(<LiveOpsStrip />);
    const chip = screen.getByTestId('tracker-api-budget-chip');
    expect(chip).toHaveTextContent('Tracker API budget 8%');
    expect(chip.className).toContain('text-theme-warning-text');
    expect(chip).toHaveAttribute('title', expect.stringContaining('120 of 1500 requests left'));
  });

  it('renders the budget chip without warning at or above 10%', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ rateLimits: { requestsLimit: 1000, requestsRemaining: 100 } }),
    });
    renderWithQuery(<LiveOpsStrip />);
    const chip = screen.getByTestId('tracker-api-budget-chip');
    expect(chip).toHaveTextContent('Tracker API budget 10%');
    expect(chip.className).not.toContain('text-theme-warning-text');
  });

  it('hides the chip when rateLimits is null', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ rateLimits: null }) });
    renderWithQuery(<LiveOpsStrip />);
    expect(screen.queryByTestId('tracker-api-budget-chip')).not.toBeInTheDocument();
    expect(screen.queryByText(/Tracker API budget/)).not.toBeInTheDocument();
  });

  it("renders 'Tracker API budget: unknown' when requestsLimit is 0", () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ rateLimits: { requestsLimit: 0, requestsRemaining: 0 } }),
    });
    renderWithQuery(<LiveOpsStrip />);
    const chip = screen.getByTestId('tracker-api-budget-chip');
    expect(chip).toHaveTextContent('Tracker API budget: unknown');
    expect(chip.textContent).not.toMatch(/NaN|Infinity|%/);
  });

  it('renders the tracker API budget chip alongside the rate-limited-until chip with distinct labels', () => {
    const reset = new Date(Date.now() + 20 * 60_000).toISOString();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        rateLimits: { requestsLimit: 1000, requestsRemaining: 50 },
        outboxEntries: [
          {
            id: 'e1',
            kind: 'comment',
            identifier: 'ENG-9',
            attempts: 1,
            rateLimitedUntil: reset,
            enqueuedAt: reset,
            nextAttemptAt: reset,
          },
        ],
      }),
    });
    renderWithQuery(<LiveOpsStrip />);
    const budget = screen.getByTestId('tracker-api-budget-chip');
    const until = screen.getByText(`Tracker rate limited until ${hhmm(reset)}`);
    expect(budget).toHaveTextContent('Tracker API budget 5%');
    expect(budget).not.toBe(until);
    expect(budget.textContent).not.toBe(until.textContent);
  });
});

// CORE-086 — chips with a natural destination link to it; the rest stay spans.
describe('LiveOpsStrip chip links (CORE-086)', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: null });
  });

  it('links non-zero attention chips to their dashboard sections', () => {
    const reset = new Date(Date.now() + 20 * 60_000).toISOString();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        retrying: [makeRetryRow()],
        paused: ['DEMO-P'],
        inputRequired: [
          {
            identifier: 'DEMO-I',
            sessionId: 's',
            state: 'input_required',
            context: 'Need approval.',
            queuedAt: reset,
          },
          {
            identifier: 'DEMO-R',
            sessionId: 't',
            state: 'pending_input_resume',
            context: 'Reply queued.',
            queuedAt: reset,
          },
        ],
        dependencyAudit: [
          { identifier: 'DEMO-B', issueState: 'Backlog', status: 'blocked', wasBlocked: true },
        ],
        outboxEntries: [
          {
            id: 'e1',
            kind: 'comment',
            identifier: 'ENG-9',
            attempts: 1,
            enqueuedAt: reset,
            nextAttemptAt: reset,
          },
        ],
      }),
    });
    renderWithQuery(<LiveOpsStrip />);
    const href = (name: RegExp) => screen.getByRole('link', { name }).getAttribute('href');
    expect(href(/^Needs input 1$/)).toBe('/#attention-inbox');
    expect(href(/^Resuming 1$/)).toBe('/#pending-resume');
    expect(href(/^Retrying 1$/)).toBe('/#retry-queue');
    expect(href(/^Paused 1$/)).toBe('/#running-sessions');
    expect(href(/^Deps 1 blocked/)).toBe('/?view=deps');
    expect(href(/^Outbox 1 pending/)).toBe('/#outbox');
  });

  it('keeps capacity, zero counts and historical counters as static spans', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot() });
    renderWithQuery(<LiveOpsStrip />);
    expect(screen.queryByRole('link', { name: /Capacity/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Retrying 0/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Automations/ })).not.toBeInTheDocument();
    expect(screen.getByText('Retrying 0').tagName).toBe('SPAN');
  });
});
