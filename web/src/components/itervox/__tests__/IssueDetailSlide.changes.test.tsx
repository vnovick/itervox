import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import type { ReactNode } from 'react';
import IssueDetailSlide from '../IssueDetailSlide';
import { routeFetch } from '../changes/__tests__/fetchMock';

vi.mock('../../../store/itervoxStore', () => ({ useItervoxStore: vi.fn() }));
vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
vi.mock('../../../queries/issues', () => {
  const mutation = () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false });
  return {
    useIssues: vi.fn(),
    useIssue: vi.fn(),
    useCancelIssue: mutation,
    useTerminateIssue: mutation,
    useResumeIssue: mutation,
    useTriggerAIReview: mutation,
    useSetIssueProfile: mutation,
    useSetIssueBackend: mutation,
    useProvideInput: mutation,
    useDismissInput: mutation,
    usePostIssueComment: mutation,
    ISSUES_KEY: ['issues'],
    ISSUE_KEY: (identifier: string) => ['issue', identifier],
  };
});

import { useItervoxStore } from '../../../store/itervoxStore';
import { authedFetch } from '../../../auth/authedFetch';
import * as issueQueries from '../../../queries/issues';

const issue = {
  identifier: 'ENG-10',
  title: 'Fix the bug',
  state: 'In Progress',
  orchestratorState: 'running' as const,
  description: 'A detailed description',
  comments: [],
  labels: [],
  priority: null,
  branchName: null,
  blockedBy: [],
  blockedByDetails: [],
  url: null,
};

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <MemoryRouter>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
}

function setup(capabilities: string[]) {
  vi.mocked(useItervoxStore).mockImplementation(((selector: (s: unknown) => unknown) =>
    selector({
      selectedIdentifier: 'ENG-10',
      setSelectedIdentifier: vi.fn(),
      snapshot: { availableProfiles: [], capabilities },
    })) as never);
  vi.mocked(issueQueries.useIssues).mockReturnValue({ data: [issue] } as never);
  vi.mocked(issueQueries.useIssue).mockReturnValue({ data: issue } as never);
}

describe('IssueDetailSlide Now panel and Changes tab (#215)', () => {
  beforeEach(() => {
    vi.mocked(authedFetch).mockReset();
    routeFetch(vi.mocked(authedFetch), {
      '/api/v1/issues/ENG-10/context': {
        body: {
          identifier: 'ENG-10',
          run: { runId: 'r', live: true, currentStep: 'Running the tests', steers: [] },
          changedFiles: [],
          handoffs: [],
        },
      },
      '/api/v1/issues/ENG-10/diff': {
        body: {
          identifier: 'ENG-10',
          base: 'main',
          mergeBase: 'abc',
          hunksAllowed: true,
          files: [{ path: 'x.go', status: 'modified', additions: 1, deletions: 0 }],
        },
      },
    });
  });

  it('shows the Now panel on Details and lazy-loads the Changes tab', async () => {
    setup(['run_context']);
    render(<IssueDetailSlide />, { wrapper });
    expect(await screen.findByText('Running the tests')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Details' })).toHaveAttribute('aria-selected', 'true');
    expect(vi.mocked(authedFetch)).not.toHaveBeenCalledWith(
      '/api/v1/issues/ENG-10/diff',
      expect.anything(),
    );

    await userEvent.click(screen.getByRole('tab', { name: 'Changes' }));
    expect(await screen.findByTestId('issue-changes-tab')).toBeInTheDocument();
    expect(screen.getByText('x.go')).toBeInTheDocument();
    expect(screen.queryByText('A detailed description')).toBeNull();
  });

  it('hides the tabs and the Now panel on a daemon without the capability', () => {
    setup([]);
    render(<IssueDetailSlide />, { wrapper });
    expect(screen.queryByRole('tab')).toBeNull();
    expect(screen.queryByTestId('issue-now-panel')).toBeNull();
    expect(vi.mocked(authedFetch)).not.toHaveBeenCalledWith(
      '/api/v1/issues/ENG-10/context',
      expect.anything(),
    );
  });
});
