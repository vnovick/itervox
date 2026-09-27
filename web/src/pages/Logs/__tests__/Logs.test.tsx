import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router';
import Logs from '../index';
import type { IssueLogEntry, TrackerIssue } from '../../../types/schemas';

// ─── Mocks ────────────────────────────────────────────────────────────────────

vi.mock('zustand/react/shallow', () => ({
  useShallow: (fn: unknown) => fn,
}));

vi.mock('../../../store/itervoxStore.ts', () => ({
  useItervoxStore: vi.fn(),
}));

const clearLogsMocks = vi.hoisted(() => ({
  mutate: vi.fn(),
  isPending: false,
}));

vi.mock('../../../queries/issues', () => ({
  useClearIssueLogs: () => ({ mutate: clearLogsMocks.mutate, isPending: clearLogsMocks.isPending }),
  useIssues: vi.fn(),
}));

vi.mock('../../../queries/logs', () => ({
  useIssueLogs: vi.fn(),
  useLogIdentifiers: vi.fn(),
}));

vi.mock('../../../components/ui/Terminal/Terminal', () => ({
  Terminal: ({ entries }: { entries: Array<{ message: string }> }) => (
    <div data-testid="terminal">
      {entries.map((e, i) => (
        <div key={i} data-testid="terminal-entry">
          {e.message}
        </div>
      ))}
    </div>
  ),
}));

import { useItervoxStore } from '../../../store/itervoxStore';
import { useIssues } from '../../../queries/issues';
import { useIssueLogs, useLogIdentifiers } from '../../../queries/logs';
import { useUIStore } from '../../../store/uiStore';

const mockuseItervoxStore = vi.mocked(useItervoxStore);
const mockUseIssues = vi.mocked(useIssues);
const mockUseIssueLogs = vi.mocked(useIssueLogs);
const mockUseLogIdentifiers = vi.mocked(useLogIdentifiers);

function makeEntry(event: string, message: string): IssueLogEntry {
  return { event, message, level: 'INFO', tool: '', time: '' } as unknown as IssueLogEntry;
}

function makeIssue(identifier: string, overrides: Partial<TrackerIssue> = {}): TrackerIssue {
  return {
    identifier,
    title: `${identifier} title`,
    state: 'In Progress',
    orchestratorState: 'idle',
    branchName: null,
    ...overrides,
  } as TrackerIssue;
}

// CORE-085 — the Logs selection lives in /logs/:identifier.
function wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/logs']}>
        <Routes>
          <Route path="/logs/:identifier?" element={children} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function setupStoreMock(
  activeIssueId: string | null = null,
  snapshotOverride?: Partial<{
    inputRequired: Array<{
      identifier: string;
      sessionId?: string;
      state?: 'input_required' | 'pending_input_resume';
      context?: string;
      queuedAt?: string;
    }>;
    paused: string[];
    running: Array<{ identifier: string; workerHost?: string; sessionId?: string }>;
    retrying: Array<{ identifier: string }>;
  }>,
) {
  mockuseItervoxStore.mockImplementation((sel: (s: any) => any) =>
    sel({
      snapshot: {
        inputRequired: snapshotOverride?.inputRequired ?? [],
        paused: snapshotOverride?.paused ?? [],
        running: snapshotOverride?.running ?? [],
        retrying: snapshotOverride?.retrying ?? [],
      },
      activeIssueId,
      setActiveIssueId: vi.fn(),
    }),
  );
}

beforeEach(() => {
  setupStoreMock(null);
  mockUseIssues.mockReturnValue({ data: [] } as ReturnType<typeof useIssues>);
  mockUseIssueLogs.mockReturnValue({ data: [], isLoading: false } as ReturnType<
    typeof useIssueLogs
  >);
  mockUseLogIdentifiers.mockReturnValue({ data: [], settled: true });
  // Reset the per-issue uiStore chip state so tests don't bleed into each
  // other (T-4 chip is Zustand-backed for persistence across navigation).
  useUIStore.setState({ logsAutomationOnly: false, logsIssueSearch: '' });
  clearLogsMocks.mutate.mockClear();
  clearLogsMocks.isPending = false;
});

describe('Logs page', () => {
  it('shows all filter chips by default', () => {
    setupStoreMock('ABC-1');
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    render(<Logs />, { wrapper });
    expect(screen.getByTestId('chip-text')).toBeInTheDocument();
    expect(screen.getByTestId('chip-action')).toBeInTheDocument();
    expect(screen.getByTestId('chip-subagent')).toBeInTheDocument();
    expect(screen.getByTestId('chip-warn')).toBeInTheDocument();
    expect(screen.getByTestId('chip-error')).toBeInTheDocument();
  });

  it('hides entries of a deactivated chip type', async () => {
    setupStoreMock('ABC-1');
    const user = userEvent.setup();
    const entries: IssueLogEntry[] = [
      makeEntry('text', 'Hello world'),
      makeEntry('action', 'Tool call'),
      makeEntry('subagent', 'Spawning subagent'),
    ];
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    mockUseIssueLogs.mockReturnValue({ data: entries, isLoading: false } as ReturnType<
      typeof useIssueLogs
    >);
    render(<Logs />, { wrapper });
    await waitFor(() => {
      expect(screen.getAllByTestId('terminal-entry')).toHaveLength(3);
    });
    await user.click(screen.getByTestId('chip-action'));
    await waitFor(() => {
      expect(screen.getAllByTestId('terminal-entry')).toHaveLength(2);
    });
  });

  it('shows entries again when a chip is re-activated', async () => {
    setupStoreMock('ABC-1');
    const user = userEvent.setup();
    const entries: IssueLogEntry[] = [makeEntry('text', 'Hello'), makeEntry('action', 'Tool call')];
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    mockUseIssueLogs.mockReturnValue({ data: entries, isLoading: false } as ReturnType<
      typeof useIssueLogs
    >);
    render(<Logs />, { wrapper });
    await waitFor(() => {
      expect(screen.getAllByTestId('terminal-entry')).toHaveLength(2);
    });
    await user.click(screen.getByTestId('chip-action'));
    await user.click(screen.getByTestId('chip-action'));
    await waitFor(() => {
      expect(screen.getAllByTestId('terminal-entry')).toHaveLength(2);
    });
  });

  it('passes correct entry messages to Terminal', async () => {
    setupStoreMock('ABC-1');
    const entries: IssueLogEntry[] = [
      makeEntry('text', 'first line'),
      makeEntry('text', 'second line'),
    ];
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    mockUseIssueLogs.mockReturnValue({ data: entries, isLoading: false } as ReturnType<
      typeof useIssueLogs
    >);
    render(<Logs />, { wrapper });
    await waitFor(() => {
      expect(screen.getByText('first line')).toBeInTheDocument();
    });
    expect(screen.getByText('second line')).toBeInTheDocument();
  });

  it('shows context strip when an issue is selected', () => {
    setupStoreMock('ABC-1');
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    mockUseIssues.mockReturnValue({ data: [makeIssue('ABC-1')] } as ReturnType<typeof useIssues>);
    render(<Logs />, { wrapper });
    expect(screen.getByTestId('logs-context-strip')).toBeInTheDocument();
  });

  it('uses snapshot input-required state for the selected issue', () => {
    setupStoreMock('ABC-1', {
      inputRequired: [{ identifier: 'ABC-1', state: 'input_required', context: 'Need approval' }],
    });
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    mockUseIssues.mockReturnValue({ data: [makeIssue('ABC-1')] } as ReturnType<typeof useIssues>);
    render(<Logs />, { wrapper });
    // Title bar + context strip (the mobile picker's <option> is excluded).
    expect(screen.getAllByText(/input required/i, { ignore: 'option' })).toHaveLength(2);
  });

  it('shows pending resume state from snapshot context for the selected issue', () => {
    setupStoreMock('ABC-1', {
      inputRequired: [
        {
          identifier: 'ABC-1',
          state: 'pending_input_resume',
          context: 'Reply received, waiting to resume.\n\nOriginal request:\nNeed approval',
        },
      ],
    });
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    mockUseIssues.mockReturnValue({ data: [makeIssue('ABC-1')] } as ReturnType<typeof useIssues>);
    render(<Logs />, { wrapper });
    expect(screen.getAllByText(/reply received/i, { ignore: 'option' })).toHaveLength(2);
  });

  it('passes tool name as prefix in entry message', async () => {
    setupStoreMock('ABC-1');
    const entries: IssueLogEntry[] = [
      {
        event: 'action',
        message: 'reading file',
        level: 'INFO',
        tool: 'Read',
        time: '',
      } as unknown as IssueLogEntry,
    ];
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    mockUseIssueLogs.mockReturnValue({ data: entries, isLoading: false } as ReturnType<
      typeof useIssueLogs
    >);
    render(<Logs />, { wrapper });
    await waitFor(() => {
      expect(screen.getByText(/Read.*reading file/)).toBeInTheDocument();
    });
  });

  it('shows live issues without logs in the sidebar', () => {
    setupStoreMock(null, { running: [{ identifier: 'ABC-1' }] });
    mockUseIssues.mockReturnValue({
      data: [makeIssue('ABC-1', { orchestratorState: 'running' })],
    } as ReturnType<typeof useIssues>);
    mockUseLogIdentifiers.mockReturnValue({ data: [], settled: true });
    render(<Logs />, { wrapper });
    expect(screen.getByRole('button', { name: 'ABC-1' })).toBeInTheDocument();
    expect(screen.getByText('1 active · 1 total')).toBeInTheDocument();
  });

  it('does not show idle issues that have no logs', () => {
    setupStoreMock(null);
    mockUseIssues.mockReturnValue({
      data: [makeIssue('ABC-1', { orchestratorState: 'idle' })],
    } as ReturnType<typeof useIssues>);
    mockUseLogIdentifiers.mockReturnValue({ data: [], settled: true });
    render(<Logs />, { wrapper });
    expect(screen.queryByRole('button', { name: 'ABC-1' })).not.toBeInTheDocument();
    expect(screen.getByText('0 active · 0 total')).toBeInTheDocument();
  });

  it('filters the sidebar issue list by identifier and title', async () => {
    setupStoreMock(null);
    const user = userEvent.setup();
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1', 'TIENG-392'], settled: true });
    mockUseIssues.mockReturnValue({
      data: [
        makeIssue('ABC-1', { title: 'Refresh auth token' }),
        makeIssue('TIENG-392', { title: 'Timeline labels are wrong' }),
      ],
    } as ReturnType<typeof useIssues>);

    render(<Logs />, { wrapper });

    expect(screen.getByRole('button', { name: 'ABC-1' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'TIENG-392' })).toBeInTheDocument();

    await user.type(screen.getByRole('searchbox', { name: /search log issues/i }), 'timeline');

    expect(screen.queryByRole('button', { name: 'ABC-1' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'TIENG-392' })).toBeInTheDocument();
    expect(screen.getByText('1 match')).toBeInTheDocument();
    // CORE-085 — a search that hides the selected row does not move the
    // selection (the URL id is checked against the unfiltered list).
    expect(screen.getByTestId('logs-context-strip')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /clear search/i }));

    expect(screen.getByRole('button', { name: 'ABC-1' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'TIENG-392' })).toBeInTheDocument();
  });

  it('shows a no-match state for issue search without claiming logs are empty', async () => {
    setupStoreMock(null);
    const user = userEvent.setup();
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    mockUseIssues.mockReturnValue({
      data: [makeIssue('ABC-1', { title: 'Refresh auth token' })],
    } as ReturnType<typeof useIssues>);

    render(<Logs />, { wrapper });

    await user.type(screen.getByRole('searchbox', { name: /search log issues/i }), 'missing');

    expect(screen.getByText('No matching issues')).toBeInTheDocument();
    expect(screen.queryByText('No issues loaded')).not.toBeInTheDocument();
  });

  it('restores branch and profile context in the header strip', () => {
    setupStoreMock('ABC-1', {
      running: [{ identifier: 'ABC-1', workerHost: 'ssh-1', sessionId: '12345678abcd' }],
    });
    mockUseIssues.mockReturnValue({
      data: [
        makeIssue('ABC-1', {
          branchName: 'feature/abc-1',
          agentProfile: 'reviewer',
        }),
      ],
    } as ReturnType<typeof useIssues>);
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    render(<Logs />, { wrapper });
    expect(screen.getByText('feature/abc-1')).toBeInTheDocument();
    expect(screen.getByText('reviewer')).toBeInTheDocument();
    expect(screen.getByText('ssh-1')).toBeInTheDocument();
  });

  it('clear requires confirmation', async () => {
    setupStoreMock('ABC-1');
    const user = userEvent.setup();
    const entries: IssueLogEntry[] = [makeEntry('text', 'Hello world')];
    mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
    mockUseIssueLogs.mockReturnValue({ data: entries, isLoading: false } as ReturnType<
      typeof useIssueLogs
    >);
    const { rerender } = render(<Logs />, { wrapper });

    // Dismissing calls mutate zero times, and restores focus to the trigger.
    await user.click(screen.getByRole('button', { name: '✕ clear' }));
    expect(screen.getByRole('button', { name: 'Yes, clear' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(clearLogsMocks.mutate).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: '✕ clear' })).toHaveFocus();

    // Accepting calls mutate exactly once, for the selected issue.
    await user.click(screen.getByRole('button', { name: '✕ clear' }));
    await user.click(screen.getByRole('button', { name: 'Yes, clear' }));
    expect(clearLogsMocks.mutate).toHaveBeenCalledTimes(1);
    expect(clearLogsMocks.mutate).toHaveBeenCalledWith('ABC-1');

    // While pending, the confirm control stays visible but disabled, and a
    // second activation does not call mutate again.
    clearLogsMocks.isPending = true;
    rerender(<Logs />);
    const pendingConfirm = screen.getByRole('button', { name: 'Clearing…' });
    expect(pendingConfirm).toBeDisabled();
    await user.click(pendingConfirm);
    expect(clearLogsMocks.mutate).toHaveBeenCalledTimes(1);

    // The outer trigger itself reflects isPending too.
    clearLogsMocks.isPending = false;
    rerender(<Logs />);
    expect(screen.getByRole('button', { name: '✕ clear' })).toHaveFocus();
    clearLogsMocks.isPending = true;
    rerender(<Logs />);
    expect(screen.getByRole('button', { name: '✕ clear' })).toBeDisabled();
  });

  describe('T-4: automation events filter chip', () => {
    it('renders an automation chip alongside the existing chips', () => {
      setupStoreMock('ABC-1');
      mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
      render(<Logs />, { wrapper });
      expect(screen.getByTestId('chip-automation-only')).toBeInTheDocument();
    });

    it('toggles to show only AUTOMATION FIRED entries when active', async () => {
      setupStoreMock('ABC-1');
      const user = userEvent.setup();
      const entries: IssueLogEntry[] = [
        makeEntry('text', 'normal log line'),
        makeEntry('text', 'AUTOMATION FIRED · pr-on-input\n  trigger: input_required'),
        makeEntry('action', 'tool call'),
      ];
      mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
      mockUseIssueLogs.mockReturnValue({ data: entries, isLoading: false } as ReturnType<
        typeof useIssueLogs
      >);
      render(<Logs />, { wrapper });
      // Default: all 3 visible.
      await waitFor(() => {
        expect(screen.getAllByTestId('terminal-entry')).toHaveLength(3);
      });
      await user.click(screen.getByTestId('chip-automation-only'));
      // After toggling: only the AUTOMATION FIRED entry remains.
      await waitFor(() => {
        const visible = screen.getAllByTestId('terminal-entry');
        expect(visible).toHaveLength(1);
        expect(visible[0].textContent).toContain('AUTOMATION FIRED');
      });
      // Toggle off restores everything.
      await user.click(screen.getByTestId('chip-automation-only'));
      await waitFor(() => {
        expect(screen.getAllByTestId('terminal-entry')).toHaveLength(3);
      });
    });

    it('exposes aria-pressed reflecting the toggled state', async () => {
      setupStoreMock('ABC-1');
      const user = userEvent.setup();
      mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
      render(<Logs />, { wrapper });
      const chip = screen.getByTestId('chip-automation-only');
      expect(chip).toHaveAttribute('aria-pressed', 'false');
      await user.click(chip);
      expect(chip).toHaveAttribute('aria-pressed', 'true');
    });
  });
  describe('CORE-082: empty, error and attention states', () => {
    it('shows filtered-out empty state', async () => {
      setupStoreMock('ABC-1');
      const user = userEvent.setup();
      mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
      mockUseIssueLogs.mockReturnValue({
        data: [makeEntry('text', 'one'), makeEntry('text', 'two')],
        isLoading: false,
      } as ReturnType<typeof useIssueLogs>);
      render(<Logs />, { wrapper });
      await user.click(screen.getByTestId('chip-text'));
      const empty = screen.getByTestId('logs-filtered-empty');
      expect(empty).toHaveTextContent('2 entries hidden by filters');
      expect(screen.queryByText(/waiting for agent output/)).not.toBeInTheDocument();
    });

    it('filtered-out empty state keeps the active chips', async () => {
      setupStoreMock('ABC-1');
      const user = userEvent.setup();
      mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
      mockUseIssueLogs.mockReturnValue({
        data: [makeEntry('text', 'plain line')],
        isLoading: false,
      } as ReturnType<typeof useIssueLogs>);
      render(<Logs />, { wrapper });
      await user.click(screen.getByTestId('chip-automation-only'));
      await user.click(screen.getByTestId('chip-warn'));
      expect(screen.getByTestId('logs-filtered-empty')).toHaveTextContent(
        '1 entry hidden by filters',
      );
      // The filter state is not reset by showing the empty state.
      expect(screen.getByTestId('chip-automation-only')).toHaveAttribute('aria-pressed', 'true');
      expect(screen.getByTestId('chip-warn')).toHaveAttribute('aria-pressed', 'false');
      expect(screen.getByTestId('chip-text')).toHaveAttribute('aria-pressed', 'true');
      expect(useUIStore.getState().logsAutomationOnly).toBe(true);
    });

    it.each([
      ['SSE path (live issue)', true],
      ['query path (idle issue)', false],
    ])('renders error state when log stream fails — %s', (_label, live) => {
      setupStoreMock('ABC-1', live ? { running: [{ identifier: 'ABC-1' }] } : {});
      mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
      mockUseIssueLogs.mockReturnValue({
        data: [],
        isLoading: false,
        isError: true,
      } as ReturnType<typeof useIssueLogs>);
      render(<Logs />, { wrapper });
      expect(mockUseIssueLogs).toHaveBeenLastCalledWith('ABC-1', live);
      const err = screen.getByTestId('logs-error-state');
      expect(err).toHaveAttribute('role', 'alert');
      expect(err).toHaveTextContent(live ? /log stream lost/i : /could not load logs/i);
      // Distinct from the empty state and from the loading/refreshing indicator.
      expect(screen.queryByText(/waiting for agent output/)).not.toBeInTheDocument();
      expect(screen.queryByTestId('logs-filtered-empty')).not.toBeInTheDocument();
      expect(screen.queryByText(/refreshing/)).not.toBeInTheDocument();
    });

    it('keeps already-received lines visible when the stream errors', () => {
      setupStoreMock('ABC-1', { running: [{ identifier: 'ABC-1' }] });
      mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
      mockUseIssueLogs.mockReturnValue({
        data: [makeEntry('text', 'kept line')],
        isLoading: false,
        isError: true,
      } as ReturnType<typeof useIssueLogs>);
      render(<Logs />, { wrapper });
      expect(screen.getByTestId('logs-error-state')).toBeInTheDocument();
      expect(screen.getByTestId('terminal-entry')).toHaveTextContent('kept line');
    });

    it('automation-only toggle has a label distinct from the automation filter chip', () => {
      setupStoreMock('ABC-1');
      mockUseLogIdentifiers.mockReturnValue({ data: ['ABC-1'], settled: true });
      render(<Logs />, { wrapper });
      const typeChip = screen.getByTestId('chip-automation');
      const onlyToggle = screen.getByTestId('chip-automation-only');
      expect(typeChip.textContent.trim()).not.toBe(onlyToggle.textContent.trim());
      expect(onlyToggle).toHaveAccessibleName(/automation only/i);
      expect(screen.getAllByRole('button', { name: /^automation$/i })).toHaveLength(1);
    });

    it('sidebar sorts input_required before running', () => {
      setupStoreMock(null, {
        running: [{ identifier: 'AAA-1' }],
        retrying: [{ identifier: 'AAA-2' }],
        inputRequired: [{ identifier: 'ZZZ-9', state: 'input_required' }],
      });
      mockUseLogIdentifiers.mockReturnValue({ data: [], settled: true });
      render(<Logs />, { wrapper });
      const sidebar = screen.getAllByRole('button', { name: /^(AAA|ZZZ)-\d$/ });
      expect(sidebar.map((b) => b.textContent)).toEqual(['ZZZ-9', 'AAA-1', 'AAA-2']);
    });
  });

  // CORE-087 — below md the sidebar is replaced by a select (jsdom renders
  // both; the md: classes decide which shows).
  it('the mobile issue picker selects an issue', async () => {
    setupStoreMock(null, { running: [{ identifier: 'AAA-1' }, { identifier: 'AAA-2' }] });
    mockUseLogIdentifiers.mockReturnValue({ data: [], settled: true });
    const user = userEvent.setup();
    render(<Logs />, { wrapper });
    const picker = screen.getByRole('combobox', { name: 'Issue' });
    await user.selectOptions(picker, 'AAA-2');
    expect(picker).toHaveValue('AAA-2');
    const sidebar = screen.getByTestId('logs-sidebar').className.split(/\s+/);
    expect(sidebar).toEqual(expect.arrayContaining(['hidden', 'md:flex']));
  });
});
