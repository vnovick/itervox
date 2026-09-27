import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import IssueDetailSlide from '../IssueDetailSlide';

vi.mock('../../../store/itervoxStore', () => ({ useItervoxStore: vi.fn() }));

vi.mock('../../../queries/issues', () => ({
  useIssues: vi.fn(),
  useIssue: vi.fn(),
  useCancelIssue: vi.fn(),
  useTerminateIssue: vi.fn(),
  useResumeIssue: vi.fn(),
  useTriggerAIReview: vi.fn(),
  useSetIssueProfile: vi.fn(),
  useSetIssueBackend: vi.fn(),
  useProvideInput: vi.fn(),
  useDismissInput: vi.fn(),
  usePostIssueComment: vi.fn(),
  ISSUES_KEY: ['issues'],
  ISSUE_KEY: (identifier: string) => ['issue', identifier],
}));

import { useItervoxStore } from '../../../store/itervoxStore';
import * as issueQueries from '../../../queries/issues';

// Cast an incomplete mock object to any expected return type without using `any`.
// The _type parameter binds T so it appears twice in the signature (param + return).
function castMock<T>(_type: T | null, val: unknown): T;
function castMock(val: unknown): unknown;
function castMock(valOrType: unknown, val?: unknown): unknown {
  return val !== undefined ? val : valOrType;
}

const mockStore = vi.mocked(useItervoxStore);
const mockUseIssues = vi.mocked(issueQueries.useIssues);
const mockUseIssue = vi.mocked(issueQueries.useIssue);
const mockUseCancelIssue = vi.mocked(issueQueries.useCancelIssue);
const mockUseTerminateIssue = vi.mocked(issueQueries.useTerminateIssue);
const mockUseResumeIssue = vi.mocked(issueQueries.useResumeIssue);
const mockUseTriggerAIReview = vi.mocked(issueQueries.useTriggerAIReview);
const mockUseSetIssueProfile = vi.mocked(issueQueries.useSetIssueProfile);
const mockUseSetIssueBackend = vi.mocked(issueQueries.useSetIssueBackend);
const mockUseProvideInput = vi.mocked(issueQueries.useProvideInput);
const mockUseDismissInput = vi.mocked(issueQueries.useDismissInput);
const mockUsePostIssueComment = vi.mocked(issueQueries.usePostIssueComment);

const baseIssue = {
  identifier: 'ENG-10',
  title: 'Fix the bug',
  state: 'In Progress',
  orchestratorState: 'running' as const,
  description: 'A detailed description',
  comments: [] as { author: string; body: string; createdAt?: string }[],
  labels: [] as string[],
  priority: null as number | null,
  branchName: null as string | null,
  blockedBy: [] as string[],
  blockedByDetails: [] as { identifier: string; state?: string; url?: string }[],
  url: null as string | null,
  agentProfile: null as string | null,
  error: undefined as string | undefined,
  ineligibleReason: undefined as string | undefined,
};

function makeWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return ({ children }: { children: React.ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
}

function setupDefaultMocks(
  selectedIdentifier: string | null,
  issueOverride?: Partial<typeof baseIssue>,
  snapshotOverride?: Record<string, unknown>,
) {
  const setSelectedIdentifier = vi.fn();
  const issue = issueOverride ? { ...baseIssue, ...issueOverride } : baseIssue;

  mockStore.mockImplementation((selector: (s: any) => any) =>
    selector({
      selectedIdentifier,
      setSelectedIdentifier,
      snapshot: { availableProfiles: [], ...snapshotOverride },
    }),
  );
  mockUseIssues.mockReturnValue(castMock({ data: [issue] }));
  mockUseIssue.mockReturnValue(castMock({ data: issue }));
  mockUseCancelIssue.mockReturnValue(
    castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  );
  mockUseTerminateIssue.mockReturnValue(
    castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  );
  mockUseResumeIssue.mockReturnValue(
    castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  );
  mockUseTriggerAIReview.mockReturnValue(
    castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  );
  mockUseSetIssueProfile.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
  mockUseSetIssueBackend.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
  mockUseProvideInput.mockReturnValue(
    castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  );
  mockUseDismissInput.mockReturnValue(
    castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
  );
  mockUsePostIssueComment.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));

  return setSelectedIdentifier;
}

describe('IssueDetailSlide', () => {
  beforeEach(() => {
    setupDefaultMocks(null);
  });

  it('renders nothing when selectedIdentifier is null', () => {
    const { container } = render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(container.firstChild).toBeNull();
  });

  it('renders nothing when issue data is not available', () => {
    mockStore.mockImplementation((selector: (s: any) => any) =>
      selector({
        selectedIdentifier: 'ENG-10',
        setSelectedIdentifier: vi.fn(),
        snapshot: { availableProfiles: [] },
      }),
    );
    mockUseIssues.mockReturnValue(castMock({ data: [] }));
    mockUseIssue.mockReturnValue(castMock({ data: undefined }));

    const { container } = render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(container.firstChild).toBeNull();
  });

  it('shows issue identifier when selected', () => {
    setupDefaultMocks('ENG-10');
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('ENG-10')).toBeInTheDocument();
  });

  it('shows issue title', () => {
    setupDefaultMocks('ENG-10');
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('Fix the bug')).toBeInTheDocument();
  });

  it('shows issue state badge', () => {
    setupDefaultMocks('ENG-10');
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('In Progress')).toBeInTheDocument();
  });

  it('shows description content', async () => {
    setupDefaultMocks('ENG-10');
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(await screen.findByText('A detailed description')).toBeInTheDocument();
  });

  it('shows status changes before the issue description', async () => {
    setupDefaultMocks('ENG-10', {
      statusChanges: [
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
      ],
    } as unknown as Partial<typeof baseIssue>);
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    const statusHeading = await screen.findByRole('heading', { name: /status changes/i });
    const descriptionHeading = screen.getByRole('heading', { name: /description/i });
    expect(statusHeading.compareDocumentPosition(descriptionHeading)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    expect(screen.getByText('dispatch-reviewer-on-pr')).toBeInTheDocument();
  });

  it('calls setSelectedIdentifier(null) when close button clicked', async () => {
    const setSelectedIdentifier = setupDefaultMocks('ENG-10');
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    const closeBtn = screen.getByRole('button', { name: /close/i });
    await userEvent.click(closeBtn);
    expect(setSelectedIdentifier).toHaveBeenCalledWith(null);
  });

  // CORE-073 (spec): the running footer says Discard, like the paused one.
  it('shows Pause and Discard buttons when running', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'running' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByRole('button', { name: '⏸ Pause' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '✕ Discard' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Stop/ })).not.toBeInTheDocument();
  });

  it('shows Resume and Discard buttons when paused', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'paused' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByRole('button', { name: '▶ Resume' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '✕ Discard' })).toBeInTheDocument();
  });

  it('shows comments when present', async () => {
    setupDefaultMocks('ENG-10', {
      comments: [{ author: 'alice', body: 'Looks good to me', createdAt: '2024-01-01T00:00:00Z' }],
    });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(await screen.findByText('Looks good to me')).toBeInTheDocument();
    expect(screen.getByText('alice')).toBeInTheDocument();
  });

  it('shows "View in tracker" link when issue has a URL', () => {
    setupDefaultMocks('ENG-10', { url: 'https://linear.app/ENG-10' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    const link = screen.getByText('View in tracker \u2192');
    expect(link).toBeInTheDocument();
    expect(link.closest('a')).toHaveAttribute('href', 'https://linear.app/ENG-10');
  });

  it('does not show "View in tracker" link when no URL', () => {
    setupDefaultMocks('ENG-10', { url: null });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.queryByText('View in tracker \u2192')).not.toBeInTheDocument();
  });

  it('shows labels as badges when present', () => {
    setupDefaultMocks('ENG-10', { labels: ['frontend', 'urgent'] });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('frontend')).toBeInTheDocument();
    expect(screen.getByText('urgent')).toBeInTheDocument();
  });

  it('shows priority badge when priority is set', () => {
    setupDefaultMocks('ENG-10', { priority: 2 });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('P2')).toBeInTheDocument();
  });

  it('does not show priority/labels row when neither is present', () => {
    setupDefaultMocks('ENG-10', { priority: null, labels: [] });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.queryByText(/^P\d$/)).not.toBeInTheDocument();
  });

  it('shows branch name when present', () => {
    setupDefaultMocks('ENG-10', { branchName: 'feat/my-branch' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('feat/my-branch')).toBeInTheDocument();
    expect(screen.getByText('Branch')).toBeInTheDocument();
  });

  it('does not show branch section when branchName is null', () => {
    setupDefaultMocks('ENG-10', { branchName: null });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.queryByText('Branch')).not.toBeInTheDocument();
  });

  it('shows blocked by section when blockedBy is non-empty', () => {
    setupDefaultMocks('ENG-10', { blockedBy: ['ENG-5', 'ENG-6'] });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('Blocked by')).toBeInTheDocument();
    expect(screen.getByText('ENG-5')).toBeInTheDocument();
    expect(screen.getByText('ENG-6')).toBeInTheDocument();
  });

  it('shows blocker details with state and links when available', () => {
    setupDefaultMocks('ENG-10', {
      blockedBy: ['ENG-5'],
      blockedByDetails: [
        {
          identifier: 'ENG-5',
          state: 'In Progress',
          url: 'https://linear.app/issue/ENG-5',
        },
      ],
    });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    const blockerLink = screen.getByRole('link', { name: 'ENG-5' });
    expect(blockerLink).toHaveAttribute('href', 'https://linear.app/issue/ENG-5');
    expect(screen.getAllByText('In Progress')).toHaveLength(2);
  });

  it('shows ineligible reason when present', () => {
    setupDefaultMocks('ENG-10', {
      ineligibleReason: 'blocked_by:ENG-5',
    });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    expect(screen.getByText('Not dispatchable')).toBeInTheDocument();
    // CORE-080 — human label from the shared table, raw reason kept beside it.
    expect(screen.getByText('Blocked by ENG-5')).toBeInTheDocument();
    expect(screen.getByText('(blocked_by:ENG-5)')).toBeInTheDocument();
  });

  it('shows "No description" when description is empty', () => {
    setupDefaultMocks('ENG-10', { description: '' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('No description')).toBeInTheDocument();
  });

  it('shows orchestratorState badge with success color for running', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'running' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('running')).toBeInTheDocument();
  });

  it('shows orchestratorState badge with warning color for retrying', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'retrying' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('retrying')).toBeInTheDocument();
  });

  // outbox #54 fast-follow: ListView/IssueDetailSlide previously had no
  // "⟳ Syncing" marker (accepted-minor D, final review) even though
  // BoardView's DraggableCard has carried it since Task 4.
  it('shows the syncing badge when the issue is in snapshot.outboxSyncing', () => {
    const setSelectedIdentifier = vi.fn();
    mockStore.mockImplementation((selector: (s: any) => any) =>
      selector({
        selectedIdentifier: 'ENG-10',
        setSelectedIdentifier,
        snapshot: { availableProfiles: [], outboxSyncing: ['ENG-10'] },
      }),
    );
    mockUseIssues.mockReturnValue(castMock({ data: [baseIssue] }));
    mockUseIssue.mockReturnValue(castMock({ data: baseIssue }));
    mockUseCancelIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseTerminateIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseResumeIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseTriggerAIReview.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseSetIssueProfile.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    mockUseSetIssueBackend.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    mockUseProvideInput.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseDismissInput.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUsePostIssueComment.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByTestId('issue-detail-syncing-badge')).toBeInTheDocument();
  });

  it('does not show the syncing badge when the issue is not in snapshot.outboxSyncing', () => {
    setupDefaultMocks('ENG-10');
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.queryByTestId('issue-detail-syncing-badge')).not.toBeInTheDocument();
  });

  it('shows Cancel retry button when retrying', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'retrying' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByRole('button', { name: '✕ Cancel retry' })).toBeInTheDocument();
  });

  it('does not show action footer when orchestratorState is idle', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'idle' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.queryByRole('button', { name: '⏸ Pause' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '▶ Resume' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '✕ Cancel retry' })).not.toBeInTheDocument();
  });

  it('shows review button when reviewerProfile is set and not running', () => {
    const setSelectedIdentifier = vi.fn();
    mockStore.mockImplementation((selector: (s: any) => any) =>
      selector({
        selectedIdentifier: 'ENG-10',
        setSelectedIdentifier,
        snapshot: { availableProfiles: [], reviewerProfile: 'reviewer', defaultBackend: 'claude' },
      }),
    );
    const issue = { ...baseIssue, orchestratorState: 'paused' as const };
    mockUseIssues.mockReturnValue(castMock({ data: [issue] }));
    mockUseIssue.mockReturnValue(castMock({ data: issue }));
    mockUseCancelIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseTerminateIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseResumeIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseTriggerAIReview.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseSetIssueProfile.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    mockUseSetIssueBackend.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    mockUseProvideInput.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseDismissInput.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUsePostIssueComment.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    const reviewBtns = screen.getAllByText(/Review/);
    expect(reviewBtns.length).toBeGreaterThanOrEqual(1);
  });

  it('does not show review button when reviewerProfile is empty', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'paused' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.queryByText(/Review/)).not.toBeInTheDocument();
  });

  it('shows backend badge defaulting to claude', () => {
    setupDefaultMocks('ENG-10');
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('claude')).toBeInTheDocument();
  });

  it('shows agent profile selector when profiles are available and issue is not in progress', () => {
    const setSelectedIdentifier = vi.fn();
    mockStore.mockImplementation((selector: (s: any) => any) =>
      selector({
        selectedIdentifier: 'ENG-10',
        setSelectedIdentifier,
        snapshot: { availableProfiles: ['fast', 'thorough'] },
      }),
    );
    const issue = { ...baseIssue, state: 'Todo', orchestratorState: 'idle' as const };
    mockUseIssues.mockReturnValue(castMock({ data: [issue] }));
    mockUseIssue.mockReturnValue(castMock({ data: issue }));
    mockUseCancelIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseTerminateIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseResumeIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseTriggerAIReview.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseSetIssueProfile.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    mockUseSetIssueBackend.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    mockUseProvideInput.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseDismissInput.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUsePostIssueComment.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('Agent Profile')).toBeInTheDocument();
    // Should show a select dropdown (not locked). CORE-056 added the
    // backend combobox beside it, so select by accessible name.
    expect(screen.getByRole('combobox', { name: 'Agent profile' })).toBeInTheDocument();
  });

  // CORE-070 — retargeted: idle issue in a custom working state, so the
  // running clause cannot mask a literal state-name comparison.
  it('shows locked profile indicator in the configured working state', () => {
    const setSelectedIdentifier = vi.fn();
    mockStore.mockImplementation((selector: (s: any) => any) =>
      selector({
        selectedIdentifier: 'ENG-10',
        setSelectedIdentifier,
        snapshot: { availableProfiles: ['fast'], workingState: 'Doing' },
      }),
    );
    const issue = {
      ...baseIssue,
      state: 'Doing',
      orchestratorState: 'idle' as const,
      agentProfile: 'fast',
    };
    mockUseIssues.mockReturnValue(castMock({ data: [issue] }));
    mockUseIssue.mockReturnValue(castMock({ data: issue }));
    mockUseCancelIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseTerminateIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseResumeIssue.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseTriggerAIReview.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseSetIssueProfile.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    mockUseSetIssueBackend.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    mockUseProvideInput.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUseDismissInput.mockReturnValue(
      castMock({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false }),
    );
    mockUsePostIssueComment.mockReturnValue(castMock({ mutate: vi.fn(), isPending: false }));
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('locked while Doing')).toBeInTheDocument();
  });

  it('shows input required UI when orchestratorState is input_required', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'input_required' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('Agent needs your input')).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/Type your reply/)).toBeInTheDocument();
    expect(screen.getByText('Reply & Resume Agent')).toBeInTheDocument();
    expect(screen.getByText('Dismiss')).toBeInTheDocument();
  });

  it('hides the reply box and keeps Dismiss when inline input is on', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'input_required' }, { inlineInput: true });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    expect(screen.getByTestId('input-required-inline-notice')).toBeInTheDocument();
    expect(screen.queryByText('Reply & Resume Agent')).not.toBeInTheDocument();
    expect(screen.queryByPlaceholderText(/Type your reply/i)).not.toBeInTheDocument();
    expect(screen.getByText('Dismiss')).toBeInTheDocument();
  });

  it('keeps the reply box when inline input is off', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'input_required' }, { inlineInput: false });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    expect(screen.getByText('Reply & Resume Agent')).toBeInTheDocument();
    expect(screen.queryByTestId('input-required-inline-notice')).not.toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: /reply to the agent/i })).toBeInTheDocument();
  });

  it('shows error markdown in input_required state when error is present', async () => {
    setupDefaultMocks('ENG-10', {
      orchestratorState: 'input_required',
      error: 'Something went wrong',
    });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(await screen.findByText('Something went wrong')).toBeInTheDocument();
  });

  it('shows pending resume UI without reply form when orchestratorState is pending_input_resume', async () => {
    setupDefaultMocks('ENG-10', {
      orchestratorState: 'pending_input_resume',
      error: 'Reply received, waiting to resume.',
    });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('Reply received')).toBeInTheDocument();
    expect(screen.queryByPlaceholderText(/Type your reply/)).not.toBeInTheDocument();
    expect(screen.queryByText('Reply & Resume Agent')).not.toBeInTheDocument();
    expect(await screen.findByText('Reply received, waiting to resume.')).toBeInTheDocument();
  });

  it('shows comment date when createdAt is present', async () => {
    setupDefaultMocks('ENG-10', {
      comments: [{ author: 'bob', body: 'LGTM', createdAt: '2024-06-15T00:00:00Z' }],
    });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(await screen.findByText('LGTM')).toBeInTheDocument();
    // Date should be formatted — check that some date text is rendered (locale-independent)
    expect(screen.getByText('bob')).toBeInTheDocument();
    // The date element should exist near the comment
    const dateEl = screen.getByText((_content, element) => {
      if (element == null) return false;
      return (
        element.tagName === 'SPAN' &&
        element.classList.contains('text-theme-muted') &&
        /2024/.test(element.textContent)
      );
    });
    expect(dateEl).toBeInTheDocument();
  });

  it('shows Unknown when comment author is empty', async () => {
    setupDefaultMocks('ENG-10', {
      comments: [{ author: '', body: 'Anonymous note' }],
    });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(await screen.findByText('Anonymous note')).toBeInTheDocument();
    expect(screen.getByText('Unknown')).toBeInTheDocument();
  });

  it('renders the comment composer for a normal issue', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'running' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    expect(screen.getByTestId('issue-comment-composer')).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: /post a comment/i })).toBeInTheDocument();
  });

  it('hides the comment composer while the issue is input_required', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'input_required' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    expect(screen.queryByTestId('issue-comment-composer')).not.toBeInTheDocument();
  });

  it('submits the composer through usePostIssueComment', () => {
    const mutate = vi.fn();
    setupDefaultMocks('ENG-10', { orchestratorState: 'running' });
    mockUsePostIssueComment.mockReturnValue(castMock({ mutate, isPending: false }));
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    fireEvent.change(screen.getByRole('textbox', { name: /post a comment/i }), {
      target: { value: '  ship it  ' },
    });
    fireEvent.click(screen.getByRole('button', { name: /post comment/i }));

    expect(mutate).toHaveBeenCalledWith(
      { identifier: 'ENG-10', body: 'ship it' },
      expect.anything(),
    );
  });

  it('Discard requires confirmation on paused and running issues', async () => {
    const user = userEvent.setup();
    const terminateMutate = vi.fn();

    // Paused → Discard.
    setupDefaultMocks('ENG-10', { orchestratorState: 'paused' });
    mockUseTerminateIssue.mockReturnValue(
      castMock({ mutate: terminateMutate, mutateAsync: vi.fn(), isPending: false }),
    );
    const { rerender } = render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    await user.click(screen.getByRole('button', { name: '✕ Discard' }));
    expect(screen.getByRole('button', { name: 'Yes, discard' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(terminateMutate).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: '✕ Discard' })).toHaveFocus();

    await user.click(screen.getByRole('button', { name: '✕ Discard' }));
    await user.click(screen.getByRole('button', { name: 'Yes, discard' }));
    expect(terminateMutate).toHaveBeenCalledTimes(1);
    expect(terminateMutate).toHaveBeenCalledWith('ENG-10');

    mockUseTerminateIssue.mockReturnValue(
      castMock({ mutate: terminateMutate, mutateAsync: vi.fn(), isPending: true }),
    );
    rerender(<IssueDetailSlide />);
    const pendingDiscard = screen.getByRole('button', { name: 'Discarding…' });
    expect(pendingDiscard).toBeDisabled();
    await user.click(pendingDiscard);
    expect(terminateMutate).toHaveBeenCalledTimes(1);

    // Settle the paused mutation and switch the SAME tree to running state,
    // reusing `rerender` so only one component tree is ever mounted.
    terminateMutate.mockClear();
    setupDefaultMocks('ENG-10', { orchestratorState: 'running' });
    mockUseTerminateIssue.mockReturnValue(
      castMock({ mutate: terminateMutate, mutateAsync: vi.fn(), isPending: false }),
    );
    rerender(<IssueDetailSlide />);

    // Running → Discard.
    await user.click(screen.getByRole('button', { name: '✕ Discard' }));
    expect(screen.getByRole('button', { name: 'Yes, discard' })).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(terminateMutate).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: '✕ Discard' })).toHaveFocus();

    await user.click(screen.getByRole('button', { name: '✕ Discard' }));
    await user.click(screen.getByRole('button', { name: 'Yes, discard' }));
    expect(terminateMutate).toHaveBeenCalledTimes(1);
    expect(terminateMutate).toHaveBeenCalledWith('ENG-10');

    mockUseTerminateIssue.mockReturnValue(
      castMock({ mutate: terminateMutate, mutateAsync: vi.fn(), isPending: true }),
    );
    rerender(<IssueDetailSlide />);
    const pendingRunningDiscard = screen.getByRole('button', { name: 'Discarding…' });
    expect(pendingRunningDiscard).toBeDisabled();
    await user.click(pendingRunningDiscard);
    expect(terminateMutate).toHaveBeenCalledTimes(1);
  });
});

// ─── CORE-070 — tracker state names come from the snapshot ───────────────────

describe('IssueDetailSlide', () => {
  const reviewButtons = () => screen.queryAllByRole('button', { name: /^🔍 Review$/ });

  it('renders a single Review button', () => {
    const fixtures: Array<[string, Partial<typeof baseIssue>, Record<string, unknown>]> = [
      ['retrying', { orchestratorState: 'retrying' as never }, {}],
      ['paused', { orchestratorState: 'paused' as never }, {}],
      ['input_required', { orchestratorState: 'input_required' as never }, {}],
      [
        'completion state',
        { state: 'QA', orchestratorState: 'idle' as never },
        { completionState: 'QA' },
      ],
      ['idle Todo', { state: 'Todo', orchestratorState: 'idle' as never }, {}],
    ];
    for (const [name, issue, snap] of fixtures) {
      setupDefaultMocks('ENG-10', issue, { reviewerProfile: 'reviewer', ...snap });
      const { unmount } = render(<IssueDetailSlide />, { wrapper: makeWrapper() });
      expect(reviewButtons(), name).toHaveLength(1);
      unmount();

      setupDefaultMocks('ENG-10', issue, { reviewerProfile: '', ...snap });
      const empty = render(<IssueDetailSlide />, { wrapper: makeWrapper() });
      expect(reviewButtons(), `${name} without reviewer`).toHaveLength(0);
      empty.unmount();
    }
  });

  it('profile lock follows configured working state', () => {
    const snap = { availableProfiles: ['fast'], workingState: 'Doing' };
    // Custom working state locks, even with the orchestrator idle.
    setupDefaultMocks('ENG-10', { state: 'Doing', orchestratorState: 'idle' as never }, snap);
    const locked = render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('locked while Doing')).toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: 'Agent profile' })).not.toBeInTheDocument();
    locked.unmount();

    // A state that merely contains "progress" is not the working state.
    setupDefaultMocks(
      'ENG-10',
      { state: 'Progress Review', orchestratorState: 'idle' as never },
      snap,
    );
    const open = render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByRole('combobox', { name: 'Agent profile' })).toBeInTheDocument();
    open.unmount();

    // An older daemon without workingState falls back to the default
    // tracker.working_state ("In Progress"), matched exactly.
    setupDefaultMocks(
      'ENG-10',
      { state: 'In Progress', orchestratorState: 'idle' as never },
      { availableProfiles: ['fast'] },
    );
    const fallback = render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('locked while In Progress')).toBeInTheDocument();
    fallback.unmount();

    // Running always locks.
    setupDefaultMocks('ENG-10', { state: 'Todo', orchestratorState: 'running' }, snap);
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.queryByRole('combobox', { name: 'Agent profile' })).not.toBeInTheDocument();
  });

  it('footer uses custom completionState', () => {
    // The footer carries run controls only. An idle issue in the configured
    // completion state ("QA") gets its Review action once, from the body —
    // not a second copy in an otherwise empty footer — and the literal
    // "In Review" name no longer changes anything.
    for (const state of ['QA', 'In Review']) {
      setupDefaultMocks(
        'ENG-10',
        { state, orchestratorState: 'idle' as never },
        { reviewerProfile: 'reviewer', completionState: 'QA' },
      );
      const { unmount } = render(<IssueDetailSlide />, { wrapper: makeWrapper() });
      expect(screen.queryByTestId('issue-detail-footer'), state).not.toBeInTheDocument();
      expect(reviewButtons(), state).toHaveLength(1);
      unmount();
    }
    setupDefaultMocks(
      'ENG-10',
      { state: 'QA', orchestratorState: 'paused' as never },
      {
        reviewerProfile: 'reviewer',
        completionState: 'QA',
      },
    );
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByTestId('issue-detail-footer')).toBeInTheDocument();
  });

  // ─── CORE-073 ───────────────────────────────────────────────────────────────

  it('Resume closes the slide on success and keeps it open on failure', async () => {
    const user = userEvent.setup();
    const resumeMutate = vi.fn();
    const setSelectedIdentifier = setupDefaultMocks('ENG-10', {
      orchestratorState: 'paused' as never,
    });
    mockUseResumeIssue.mockReturnValue(
      castMock({ mutate: resumeMutate, mutateAsync: vi.fn(), isPending: false }),
    );
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });

    // Failure: the mutation reports an error; the slide stays open.
    resumeMutate.mockImplementationOnce(
      (_id: string, opts?: { onError?: (e: Error) => void; onSettled?: () => void }) => {
        opts?.onError?.(new Error('boom'));
        opts?.onSettled?.();
      },
    );
    await user.click(screen.getByRole('button', { name: '▶ Resume' }));
    expect(resumeMutate).toHaveBeenCalledWith('ENG-10', expect.anything());
    expect(setSelectedIdentifier).not.toHaveBeenCalled();

    // Success: the slide closes only once the server accepted the resume.
    resumeMutate.mockImplementationOnce((_id: string, opts?: { onSuccess?: () => void }) => {
      expect(setSelectedIdentifier).not.toHaveBeenCalled();
      opts?.onSuccess?.();
    });
    await user.click(screen.getByRole('button', { name: '▶ Resume' }));
    expect(setSelectedIdentifier).toHaveBeenCalledWith(null);
  });
});
