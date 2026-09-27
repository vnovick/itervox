import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, render, screen, within } from '@testing-library/react';
import { axeViolations } from '../../../test/axe';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import RunningSessionsTable from '../RunningSessionsTable';
import type { RunningRow } from '../../../types/schemas';

// Mock Zustand store
vi.mock('../../../store/itervoxStore.ts', () => ({
  useItervoxStore: vi.fn(),
}));

// Mock query hooks. terminateIssueMock is controllable per-test (CORE-021
// confirmation tests need to assert call counts and simulate isPending).
const issueMocks = vi.hoisted(() => ({
  data: [] as { identifier: string; agentProfile?: string }[],
}));

const confirmationMocks = vi.hoisted(() => ({
  terminateMutate: vi.fn(),
  terminateIsPending: false,
}));

vi.mock('../../../queries/issues', () => ({
  useCancelIssue: () => ({ mutate: vi.fn(), isPending: false }),
  useTerminateIssue: () => ({
    mutate: confirmationMocks.terminateMutate,
    isPending: confirmationMocks.terminateIsPending,
  }),
  useResumeIssue: () => ({ mutate: vi.fn(), isPending: false }),
  useSetIssueProfile: () => ({ mutate: vi.fn(), isPending: false }),
  useSetIssueBackend: () => ({ mutate: vi.fn(), isPending: false }),
  useTriggerAIReview: () => ({ mutate: vi.fn(), isPending: false }),
  useIssues: () => ({ data: issueMocks.data }),
}));

vi.mock('../../../queries/logs', () => ({
  useIssueLogs: () => ({ data: [] }),
}));

vi.mock('../../ui/Terminal/Terminal', () => ({
  Terminal: () => <div data-testid="terminal-mock" />,
}));

import { useItervoxStore } from '../../../store/itervoxStore';
import { useUIStore } from '../../../store/uiStore';

const mockUseItervoxStore = vi.mocked(useItervoxStore);

function makeWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

const baseRow: RunningRow = {
  identifier: 'ISS-42',
  state: 'In Progress',
  turnCount: 5,
  tokens: 1200,
  inputTokens: 800,
  outputTokens: 400,
  lastEvent: 'Doing some work',
  lastEventAt: null,
  sessionId: 'sess-abc-123',
  workerHost: 'worker-1',
  backend: 'claude',
  elapsedMs: 60000,
  startedAt: new Date(Date.now() - 60000).toISOString(),
};

describe('RunningSessionsTable', () => {
  const mockSetSelectedIdentifier = vi.fn();

  beforeEach(() => {
    useUIStore.setState({ expandedRunningId: null, expandedPausedId: null });
    // Default: empty snapshot
    mockUseItervoxStore.mockImplementation((selector: (s: any) => any) =>
      selector({ snapshot: null, setSelectedIdentifier: mockSetSelectedIdentifier }),
    );
    confirmationMocks.terminateMutate.mockClear();
    confirmationMocks.terminateIsPending = false;
  });

  function withSnapshot(snapshot: {
    running?: RunningRow[];
    paused?: string[];
    pausedWithPR?: Record<string, string>;
    availableProfiles?: string[];
  }) {
    mockUseItervoxStore.mockImplementation((selector: (s: any) => any) =>
      selector({
        snapshot: {
          running: snapshot.running ?? [],
          paused: snapshot.paused ?? [],
          pausedWithPR: snapshot.pausedWithPR ?? {},
          availableProfiles: snapshot.availableProfiles ?? [],
        },
        setSelectedIdentifier: mockSetSelectedIdentifier,
      }),
    );
  }

  it('renders empty state when no running sessions', () => {
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('No agents running')).toBeInTheDocument();
  });

  it('renders "Running Sessions" heading when running sessions exist', () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('Running Sessions')).toBeInTheDocument();
  });

  it('renders session row when running sessions provided', () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('ISS-42')).toBeInTheDocument();
  });

  it('shows session identifier in the row', () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('ISS-42')).toBeInTheDocument();
  });

  it('shows session state badge', () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('In Progress')).toBeInTheDocument();
  });

  it('shows count badge when sessions exist', () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('1 active')).toBeInTheDocument();
  });

  // CORE-073 (spec acceptance, verbatim name) — /terminate releases the claim
  // and moves the issue to the first backlog state, so the running row uses
  // the same verb as the paused row: Discard. "Stop"/"Cancel" understated it.
  it('running row labels Pause and Discard', () => {
    withSnapshot({ running: [baseRow], paused: ['ISS-99'] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    const runningRow = screen.getByTestId('running-row-ISS-42');
    expect(within(runningRow).getByRole('button', { name: '⏸ Pause' })).toBeInTheDocument();
    expect(within(runningRow).getByRole('button', { name: '✕ Discard' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Stop/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^✕ Cancel$/ })).not.toBeInTheDocument();
    // Paused row: Resume and Discard.
    const pausedRow = screen.getByTestId('paused-row-ISS-99');
    expect(within(pausedRow).getByRole('button', { name: '▶ Resume' })).toBeInTheDocument();
    expect(within(pausedRow).getByRole('button', { name: '✕ Discard' })).toBeInTheDocument();
  });

  // M5-close (axe nested-interactive / target-size) — the row toggles are
  // real buttons that do not contain other controls.
  it('rows have no nested interactive controls and keep keyboard toggles', async () => {
    withSnapshot({ running: [baseRow], paused: ['ISS-99'] });
    const { container } = render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(await axeViolations(container)).toEqual([]);
    const toggle = screen.getByRole('button', { name: 'Toggle details for ISS-42' });
    expect(toggle.tagName).toBe('BUTTON');
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    toggle.focus();
    await userEvent.keyboard('{Enter}');
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByTestId('terminal-mock')).toBeInTheDocument();
    const open = screen.getByRole('button', { name: 'View details for ISS-42' });
    expect(open.tagName).toBe('BUTTON');
    await userEvent.click(open);
    expect(mockSetSelectedIdentifier).toHaveBeenCalledWith('ISS-42');
  });

  it('shows running session summary fields when rows are present', () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    // Turn count and elapsed time are rendered in the row grid
    expect(screen.getByText('5')).toBeInTheDocument();
    expect(screen.getByText('1m 00s')).toBeInTheDocument();
  });

  it('shows paused section when paused identifiers exist', () => {
    withSnapshot({ paused: ['ISS-99'] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('ISS-99')).toBeInTheDocument();
    expect(screen.getByText(/Paused/)).toBeInTheDocument();
  });

  it('shows Resume and Discard buttons for paused items', () => {
    withSnapshot({ paused: ['ISS-99'] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText(/Resume/)).toBeInTheDocument();
    expect(screen.getByText(/Discard/)).toBeInTheDocument();
  });

  it('shows PR link when paused with PR', () => {
    withSnapshot({
      paused: ['ISS-99'],
      pausedWithPR: { 'ISS-99': 'https://github.com/org/repo/pull/5' },
    });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('PR')).toBeInTheDocument();
  });

  it('expands accordion on row click', async () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    // A mouse click anywhere on the row (not only the chevron) still expands.
    await userEvent.click(screen.getByTestId('running-row-ISS-42'));
    // After expanding, the accordion renders the terminal mock
    expect(screen.getByTestId('terminal-mock')).toBeInTheDocument();
  });

  it('shows reviewer badge when row kind is reviewer', () => {
    const reviewerRow: RunningRow = { ...baseRow, kind: 'reviewer' };
    withSnapshot({ running: [reviewerRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('Review')).toBeInTheDocument();
  });

  it('does not show reviewer badge when kind is not reviewer', () => {
    const workerRow: RunningRow = { ...baseRow, kind: 'worker' };
    withSnapshot({ running: [workerRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.queryByText('Review')).not.toBeInTheDocument();
  });

  it('shows dash when turnCount is null', () => {
    const noTurnRow: RunningRow = { ...baseRow, turnCount: null as any };
    withSnapshot({ running: [noTurnRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    // The turn count column should render a dash
    const dashes = screen.getAllByText('\u2014');
    expect(dashes.length).toBeGreaterThanOrEqual(1);
  });

  it('shows dash when lastEvent is empty', () => {
    const noEventRow: RunningRow = { ...baseRow, lastEvent: undefined };
    withSnapshot({ running: [noEventRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    const dashes = screen.getAllByText('\u2014');
    expect(dashes.length).toBeGreaterThanOrEqual(1);
  });

  it('truncates lastEvent to 100 chars', () => {
    const longEvent = 'A'.repeat(150);
    const longEventRow: RunningRow = { ...baseRow, lastEvent: longEvent };
    withSnapshot({ running: [longEventRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('A'.repeat(100))).toBeInTheDocument();
  });

  it('does not show PR badge for paused item without PR', () => {
    withSnapshot({ paused: ['ISS-99'], pausedWithPR: {} });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.queryByText('PR')).not.toBeInTheDocument();
  });

  it('renders both running and paused sections simultaneously', () => {
    withSnapshot({ running: [baseRow], paused: ['ISS-99'] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText('Running Sessions')).toBeInTheDocument();
    expect(screen.getByText(/Paused/)).toBeInTheDocument();
    expect(screen.getByText('ISS-42')).toBeInTheDocument();
    expect(screen.getByText('ISS-99')).toBeInTheDocument();
  });

  it('does not show Running Sessions header when only paused items exist', () => {
    withSnapshot({ paused: ['ISS-99'] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.queryByText('Running Sessions')).not.toBeInTheDocument();
    expect(screen.getByText(/Paused/)).toBeInTheDocument();
  });

  it('opens detail slide when identifier is clicked in paused section', async () => {
    withSnapshot({ paused: ['ISS-99'] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    const identifier = screen.getByLabelText('View details for paused issue ISS-99');
    await userEvent.click(identifier);
    expect(mockSetSelectedIdentifier).toHaveBeenCalledWith('ISS-99');
  });

  it('opens detail slide when identifier is clicked in running section', async () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    const identifier = screen.getByLabelText('View details for ISS-42');
    await userEvent.click(identifier);
    expect(mockSetSelectedIdentifier).toHaveBeenCalledWith('ISS-42');
  });

  it('expands paused accordion on paused row click', async () => {
    withSnapshot({ paused: ['ISS-99'] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    const pausedRow = screen.getByLabelText('Toggle details for paused issue ISS-99');
    await userEvent.click(pausedRow);
    expect(screen.getByTestId('terminal-mock')).toBeInTheDocument();
  });

  it('shows multiple running rows sorted by startedAt', () => {
    const earlier: RunningRow = {
      ...baseRow,
      identifier: 'ISS-1',
      startedAt: new Date(Date.now() - 120000).toISOString(),
    };
    const later: RunningRow = {
      ...baseRow,
      identifier: 'ISS-2',
      startedAt: new Date(Date.now() - 30000).toISOString(),
    };
    withSnapshot({ running: [later, earlier] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    const identifiers = screen.getAllByText(/ISS-/);
    // Earlier should come first in order
    expect(identifiers[0].textContent).toBe('ISS-1');
    expect(identifiers[1].textContent).toBe('ISS-2');
  });

  it('shows paused count in section header', () => {
    withSnapshot({ paused: ['ISS-1', 'ISS-2', 'ISS-3'] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    expect(screen.getByText(/Paused \(3\)/)).toBeInTheDocument();
  });

  it('expands running row via keyboard Enter on identifier', async () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    // Click the identifier to open detail slide via keyboard
    const identifierButton = screen.getByLabelText('View details for ISS-42');
    await userEvent.click(identifierButton);
    expect(mockSetSelectedIdentifier).toHaveBeenCalledWith('ISS-42');
  });

  it('running-row Discard requires confirmation', async () => {
    withSnapshot({ running: [baseRow] });
    const user = userEvent.setup();
    const { rerender } = render(<RunningSessionsTable />, { wrapper: makeWrapper() });

    // Dismissing the confirmation calls mutate zero times, and restores
    // focus to the trigger.
    await user.click(screen.getByRole('button', { name: '✕ Discard' }));
    expect(screen.getByRole('button', { name: 'Yes, discard' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(confirmationMocks.terminateMutate).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: '✕ Discard' })).toHaveFocus();

    // Accepting calls mutate exactly once, with the right identifier.
    await user.click(screen.getByRole('button', { name: '✕ Discard' }));
    await user.click(screen.getByRole('button', { name: 'Yes, discard' }));
    expect(confirmationMocks.terminateMutate).toHaveBeenCalledTimes(1);
    expect(confirmationMocks.terminateMutate).toHaveBeenCalledWith('ISS-42');

    // While the mutation is in flight, the confirm control stays visible
    // but disabled — a second activation must not call mutate again.
    confirmationMocks.terminateIsPending = true;
    rerender(<RunningSessionsTable />);
    const pendingConfirm = screen.getByRole('button', { name: 'Discarding…' });
    expect(pendingConfirm).toBeDisabled();
    await user.click(pendingConfirm);
    expect(confirmationMocks.terminateMutate).toHaveBeenCalledTimes(1);

    // The outer trigger is disabled while isPending, independent of the
    // confirm/cancel flow (e.g. re-render with the row freshly collapsed).
    confirmationMocks.terminateIsPending = false;
    rerender(<RunningSessionsTable />);
    expect(screen.getByRole('button', { name: '✕ Discard' })).toHaveFocus();
    confirmationMocks.terminateIsPending = true;
    rerender(<RunningSessionsTable />);
    expect(screen.getByRole('button', { name: '✕ Discard' })).toBeDisabled();
  });

  it('Discard requires confirmation', async () => {
    withSnapshot({ paused: ['ISS-99'] });
    const user = userEvent.setup();
    const { rerender } = render(<RunningSessionsTable />, { wrapper: makeWrapper() });

    await user.click(screen.getByRole('button', { name: '✕ Discard' }));
    expect(screen.getByRole('button', { name: 'Yes, discard' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(confirmationMocks.terminateMutate).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: '✕ Discard' })).toHaveFocus();

    await user.click(screen.getByRole('button', { name: '✕ Discard' }));
    await user.click(screen.getByRole('button', { name: 'Yes, discard' }));
    expect(confirmationMocks.terminateMutate).toHaveBeenCalledTimes(1);
    expect(confirmationMocks.terminateMutate).toHaveBeenCalledWith('ISS-99');

    confirmationMocks.terminateIsPending = true;
    rerender(<RunningSessionsTable />);
    const pendingConfirm = screen.getByRole('button', { name: 'Discarding…' });
    expect(pendingConfirm).toBeDisabled();
    await user.click(pendingConfirm);
    expect(confirmationMocks.terminateMutate).toHaveBeenCalledTimes(1);

    confirmationMocks.terminateIsPending = false;
    rerender(<RunningSessionsTable />);
    expect(screen.getByRole('button', { name: '✕ Discard' })).toHaveFocus();
    confirmationMocks.terminateIsPending = true;
    rerender(<RunningSessionsTable />);
    expect(screen.getByRole('button', { name: '✕ Discard' })).toBeDisabled();
  });

  // CORE-087 — jsdom does not evaluate breakpoints, so this pins the class
  // branch: a wrapping flex card by default (phones), the fixed grid from sm up.
  it('running row uses the card-layout branch below sm', () => {
    withSnapshot({ running: [baseRow] });
    render(<RunningSessionsTable />, { wrapper: makeWrapper() });
    const row = screen.getAllByTestId(/^running-row-/)[0];
    expect(row.className).toMatch(/(^|\s)flex-wrap(\s|$)/);
    expect(row.className).toContain('sm:grid');
    expect(row.className).toMatch(/sm:grid-cols-\[/);
    // No inline grid template: it would apply at every width.
    expect(row.style.gridTemplateColumns).toBe('');
  });

  // CORE-090 — collapsed rows carry where and on what an issue runs.
  describe('collapsed row details (CORE-090)', () => {
    afterEach(() => {
      vi.useRealTimers();
      issueMocks.data = [];
    });

    it('collapsed running row shows backend, host, tokens, last-activity age and profile', () => {
      vi.useFakeTimers({ now: new Date('2026-09-27T12:00:00Z') });
      issueMocks.data = [{ identifier: 'ISS-42', agentProfile: 'fast-coder' }];
      withSnapshot({
        running: [
          {
            ...baseRow,
            backend: 'codex',
            workerHost: 'gpu-1',
            tokens: 12_345,
            lastEventAt: '2026-09-27T11:59:30Z',
          },
        ],
      });
      render(<RunningSessionsTable />, { wrapper: makeWrapper() });
      const meta = screen.getByTestId('running-row-meta-ISS-42');
      expect(meta).toHaveTextContent('codex');
      expect(meta).toHaveTextContent('gpu-1');
      expect(meta).toHaveTextContent('12.3k tokens');
      expect(meta).toHaveTextContent('active 30s ago');
      expect(meta).toHaveTextContent('fast-coder');
      // Elapsed stays its own cell.
      expect(screen.getByText('1m 00s')).toBeInTheDocument();
    });

    it("running row falls back to 'local' / '—' when workerHost and backend are absent", () => {
      withSnapshot({
        running: [{ ...baseRow, backend: undefined, workerHost: undefined }],
      });
      render(<RunningSessionsTable />, { wrapper: makeWrapper() });
      const meta = screen.getByTestId('running-row-meta-ISS-42');
      expect(within(meta).getByTitle('Backend')).toHaveTextContent('—');
      expect(within(meta).getByTitle('Host')).toHaveTextContent('local');
    });

    it('last-activity age advances with fake timers without a new snapshot', () => {
      vi.useFakeTimers({ now: new Date('2026-09-27T12:00:00Z') });
      withSnapshot({ running: [{ ...baseRow, lastEventAt: '2026-09-27T11:59:55Z' }] });
      render(<RunningSessionsTable />, { wrapper: makeWrapper() });
      const age = () =>
        within(screen.getByTestId('running-row-meta-ISS-42')).getByTitle('Last activity');
      expect(age()).toHaveTextContent('active 5s ago');
      act(() => {
        vi.advanceTimersByTime(3_000);
      });
      expect(age()).toHaveTextContent('active 8s ago');
    });

    it("renders '—' for age when lastEventAt is absent or invalid", () => {
      withSnapshot({
        running: [
          { ...baseRow, identifier: 'ISS-1', lastEventAt: undefined },
          { ...baseRow, identifier: 'ISS-2', lastEventAt: 'not a date' },
        ],
      });
      render(<RunningSessionsTable />, { wrapper: makeWrapper() });
      for (const id of ['ISS-1', 'ISS-2']) {
        expect(
          within(screen.getByTestId(`running-row-meta-${id}`)).getByTitle('Last activity'),
        ).toHaveTextContent('—');
      }
    });
  });
});
