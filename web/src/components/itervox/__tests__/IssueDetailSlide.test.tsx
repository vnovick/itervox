import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
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
) {
  const setSelectedIdentifier = vi.fn();
  const issue = issueOverride ? { ...baseIssue, ...issueOverride } : baseIssue;

  mockStore.mockImplementation((selector: (s: any) => any) =>
    selector({
      selectedIdentifier,
      setSelectedIdentifier,
      snapshot: { availableProfiles: [] },
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

  it('shows Pause Agent and Cancel Agent buttons when running', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'running' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText(/Pause Agent/)).toBeInTheDocument();
    expect(screen.getByText(/Cancel Agent/)).toBeInTheDocument();
  });

  it('shows Resume Agent and Discard buttons when paused', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'paused' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText(/Resume Agent/)).toBeInTheDocument();
    expect(screen.getByText(/Discard/)).toBeInTheDocument();
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
    expect(screen.getByText('blocked_by:ENG-5')).toBeInTheDocument();
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

  it('shows Cancel Retry button when retrying', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'retrying' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText(/Cancel Retry/)).toBeInTheDocument();
  });

  it('does not show action footer when orchestratorState is idle', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'idle' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.queryByText(/Pause Agent/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Resume Agent/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Cancel Retry/)).not.toBeInTheDocument();
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
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('Agent Profile')).toBeInTheDocument();
    // Should show a select dropdown (not locked)
    expect(screen.getByRole('combobox')).toBeInTheDocument();
  });

  it('shows locked profile indicator when state includes progress', () => {
    const setSelectedIdentifier = vi.fn();
    mockStore.mockImplementation((selector: (s: any) => any) =>
      selector({
        selectedIdentifier: 'ENG-10',
        setSelectedIdentifier,
        snapshot: { availableProfiles: ['fast'] },
      }),
    );
    const issue = {
      ...baseIssue,
      state: 'In Progress',
      orchestratorState: 'running' as const,
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
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('locked while In Progress')).toBeInTheDocument();
  });

  it('shows input required UI when orchestratorState is input_required', () => {
    setupDefaultMocks('ENG-10', { orchestratorState: 'input_required' });
    render(<IssueDetailSlide />, { wrapper: makeWrapper() });
    expect(screen.getByText('Agent needs your input')).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/Type your reply/)).toBeInTheDocument();
    expect(screen.getByText('Reply & Resume Agent')).toBeInTheDocument();
    expect(screen.getByText('Dismiss')).toBeInTheDocument();
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
});
