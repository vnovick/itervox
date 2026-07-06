import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import Timeline from '../index';
import { useUIStore } from '../../../store/uiStore';

const detailPanelSpy = vi.hoisted(() => vi.fn());

vi.mock('../../../components/common/PageMeta', () => ({
  default: () => null,
}));

vi.mock('../../../store/itervoxStore.ts', () => ({
  useItervoxStore: vi.fn(),
}));

vi.mock('../../../queries/issues', () => ({
  useClearIssueLogs: () => ({ mutate: vi.fn(), isPending: false }),
  useClearIssueSubLogs: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock('../../../queries/logs', () => ({
  useIssueLogs: () => ({ data: [] }),
  useSubagentLogs: () => ({ data: [] }),
}));

vi.mock('../components/TimelineSidebar', () => ({
  TimelineSidebar: ({
    issueGroups,
  }: {
    issueGroups: Array<{ identifier: string; runs: unknown[] }>;
  }) => (
    <div data-testid="timeline-sidebar">
      {issueGroups.map((g) => (
        <div key={g.identifier} data-testid="timeline-row" data-run-count={g.runs.length}>
          {g.identifier}
        </div>
      ))}
    </div>
  ),
}));

vi.mock('../components/TimelineDetailPanel', () => ({
  TimelineDetailPanel: (props: Record<string, unknown>) => {
    detailPanelSpy(props);
    return <div data-testid="timeline-detail" />;
  },
}));

import { useItervoxStore } from '../../../store/itervoxStore';
const mockStore = vi.mocked(useItervoxStore);

function setupStore(
  snapshot: {
    running?: Array<Record<string, unknown>>;
    history?: Array<Record<string, unknown>>;
  },
  activeIssueId: string | null = null,
) {
  const fullSnapshot = {
    running: snapshot.running ?? [],
    history: snapshot.history ?? [],
    currentAppSessionId: 'app-1',
  };
  mockStore.mockImplementation((sel: (s: unknown) => unknown) =>
    sel({
      snapshot: fullSnapshot,
      activeIssueId,
      setActiveIssueId: vi.fn(),
    }),
  );
}

function wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return React.createElement(QueryClientProvider, { client: qc }, children);
}

beforeEach(() => {
  useUIStore.setState({ timelineAutomationOnly: false });
  detailPanelSpy.mockClear();
});

describe('Timeline automation chip (T-5)', () => {
  it('renders the chip in the filter bar', () => {
    setupStore({});
    render(<Timeline />, { wrapper });
    expect(screen.getByTestId('timeline-chip-automation')).toBeInTheDocument();
  });

  it('disables the chip with a tooltip when there are no automation runs', () => {
    setupStore({
      running: [
        {
          identifier: 'ENG-1',
          state: 'In Progress',
          startedAt: new Date().toISOString(),
          elapsedMs: 1000,
          turnCount: 1,
          tokens: 100,
          inputTokens: 80,
          outputTokens: 20,
          lastEvent: '',
          lastEventAt: null,
          sessionId: 'sess-1',
          workerHost: 'local',
          backend: 'claude',
        },
      ],
    });
    render(<Timeline />, { wrapper });
    const chip = screen.getByTestId('timeline-chip-automation');
    expect(chip).toBeDisabled();
    expect(chip.getAttribute('title')).toContain('No automation runs');
  });

  it('hides manual runs when toggled on', () => {
    const now = Date.now();
    setupStore({
      running: [
        {
          identifier: 'ENG-MANUAL',
          state: 'In Progress',
          startedAt: new Date(now).toISOString(),
          elapsedMs: 1000,
          turnCount: 1,
          tokens: 0,
          inputTokens: 0,
          outputTokens: 0,
          lastEvent: '',
          lastEventAt: null,
          sessionId: 'sess-manual',
          workerHost: 'local',
          backend: 'claude',
        },
      ],
      history: [
        {
          identifier: 'ENG-AUTO',
          startedAt: new Date(now - 5_000).toISOString(),
          finishedAt: new Date(now - 1_000).toISOString(),
          elapsedMs: 4_000,
          turnCount: 2,
          tokens: 200,
          inputTokens: 150,
          outputTokens: 50,
          status: 'succeeded',
          backend: 'claude',
          sessionId: 'sess-auto',
          automationId: 'pr-on-input',
          triggerType: 'input_required',
        },
      ],
    });
    render(<Timeline />, { wrapper });

    // Chip enabled because at least one history row carries an automationId.
    const chip = screen.getByTestId('timeline-chip-automation');
    expect(chip).not.toBeDisabled();

    // Default state: both rows visible in the sidebar.
    expect(screen.getByText('ENG-MANUAL')).toBeInTheDocument();
    expect(screen.getByText('ENG-AUTO')).toBeInTheDocument();

    fireEvent.click(chip);

    expect(useUIStore.getState().timelineAutomationOnly).toBe(true);
    // After toggle: only the automation-dispatched row remains.
    expect(screen.queryByText('ENG-MANUAL')).not.toBeInTheDocument();
    expect(screen.getByText('ENG-AUTO')).toBeInTheDocument();
  });

  it('persists chip state across re-renders via uiStore', () => {
    setupStore({
      history: [
        {
          identifier: 'ENG-AUTO',
          startedAt: new Date().toISOString(),
          finishedAt: new Date().toISOString(),
          elapsedMs: 1000,
          turnCount: 1,
          tokens: 0,
          inputTokens: 0,
          outputTokens: 0,
          status: 'succeeded',
          backend: 'claude',
          sessionId: 'sess-auto',
          automationId: 'pr-on-input',
          triggerType: 'input_required',
        },
      ],
    });
    useUIStore.setState({ timelineAutomationOnly: true });
    render(<Timeline />, { wrapper });
    const chip = screen.getByTestId('timeline-chip-automation');
    expect(chip).toHaveAttribute('aria-pressed', 'true');
  });

  // G-13 relay — during the ~5s stale-running window (useStableValue) the same
  // sessionId can appear in both `running` and `history`; the sidebar run
  // count must not double-count it.
  it('dedups a session present in both running and history into one sidebar run', () => {
    const now = Date.now();
    setupStore({
      running: [
        {
          identifier: 'ENG-DUP',
          state: 'In Progress',
          startedAt: new Date(now - 10_000).toISOString(),
          elapsedMs: 10_000,
          turnCount: 3,
          tokens: 100,
          inputTokens: 80,
          outputTokens: 20,
          lastEvent: '',
          lastEventAt: null,
          sessionId: 'sess-dup',
          workerHost: 'local',
          backend: 'claude',
        },
      ],
      history: [
        {
          identifier: 'ENG-DUP',
          startedAt: new Date(now - 10_000).toISOString(),
          finishedAt: new Date(now - 1_000).toISOString(),
          elapsedMs: 9_000,
          turnCount: 3,
          tokens: 100,
          inputTokens: 80,
          outputTokens: 20,
          status: 'succeeded',
          backend: 'claude',
          sessionId: 'sess-dup',
        },
      ],
    });
    render(<Timeline />, { wrapper });

    const row = screen.getByText('ENG-DUP');
    expect(row).toHaveAttribute('data-run-count', '1');
  });

  it('keeps the chart viewport on the full selected issue span while a run is expanded', async () => {
    const firstStart = new Date(2026, 0, 1, 18, 50).getTime();
    const firstEnd = firstStart + 22_000;
    const secondStart = new Date(2026, 0, 1, 21, 2).getTime();
    const secondEnd = secondStart + 124_000;

    setupStore(
      {
        history: [
          {
            identifier: 'TIENG-392',
            startedAt: new Date(firstStart).toISOString(),
            finishedAt: new Date(firstEnd).toISOString(),
            elapsedMs: 22_000,
            turnCount: 1,
            tokens: 8,
            inputTokens: 0,
            outputTokens: 0,
            status: 'succeeded',
            backend: 'claude',
            sessionId: 'sess-1',
          },
          {
            identifier: 'TIENG-392',
            startedAt: new Date(secondStart).toISOString(),
            finishedAt: new Date(secondEnd).toISOString(),
            elapsedMs: 124_000,
            turnCount: 1,
            tokens: 63,
            inputTokens: 0,
            outputTokens: 0,
            status: 'succeeded',
            backend: 'claude',
            sessionId: 'sess-2',
          },
        ],
      },
      'TIENG-392',
    );

    render(<Timeline />, { wrapper });

    await waitFor(() => {
      const latestProps = detailPanelSpy.mock.calls.at(-1)?.[0] as
        | { expandedRunAt?: string | null; viewStart?: number; viewEnd?: number }
        | undefined;
      expect(latestProps?.expandedRunAt).toBe(new Date(secondStart).toISOString());
      expect(latestProps?.viewStart).toBeLessThanOrEqual(firstStart - 120_000);
      expect(latestProps?.viewEnd).toBeGreaterThanOrEqual(secondEnd + 120_000);
    });
  });
});
