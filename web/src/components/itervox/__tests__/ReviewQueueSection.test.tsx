import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { ReviewQueueSection } from '../ReviewQueueSection';

const mockMutate = vi.fn();

vi.mock('../../../store/itervoxStore.ts', () => ({
  useItervoxStore: vi.fn(),
}));

vi.mock('../../../queries/issues', () => ({
  useIssues: () => ({ data: mockIssues }),
  useTriggerAIReview: () => ({ mutate: mockMutate, isPending: false }),
}));

vi.mock('zustand/react/shallow', () => ({
  useShallow: <T,>(fn: T): T => fn,
}));

import { useItervoxStore } from '../../../store/itervoxStore';

const mockStore = vi.mocked(useItervoxStore);

let mockIssues: Array<{ identifier: string; title: string; state: string }> = [];

function setupStore(
  overrides: {
    reviewerProfile?: string;
    completionState?: string;
    running?: Array<{
      identifier: string;
      state: string;
      turnCount: number;
      tokens: number;
      inputTokens: number;
      outputTokens: number;
      elapsedMs: number;
      startedAt: string;
      kind?: string;
    }>;
    history?: Array<{
      identifier: string;
      startedAt: string;
      finishedAt: string;
      elapsedMs: number;
      turnCount: number;
      tokens: number;
      inputTokens: number;
      outputTokens: number;
      status: string;
      sessionId?: string;
      kind?: string;
    }>;
  } = {},
) {
  mockStore.mockImplementation((selector: any) =>
    selector({
      snapshot: {
        reviewerProfile: overrides.reviewerProfile ?? '',
        completionState: overrides.completionState ?? '',
        running: overrides.running ?? [],
        history: overrides.history ?? [],
      },
    }),
  );
}

describe('ReviewQueueSection', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockIssues = [];
  });

  it('returns null when no reviewer profile is configured', () => {
    setupStore({ reviewerProfile: '' });
    const { container } = render(<ReviewQueueSection />);
    expect(container.firstChild).toBeNull();
  });

  it('renders the Review Queue header when reviewer profile exists', () => {
    setupStore({ reviewerProfile: 'code-reviewer' });
    render(<ReviewQueueSection />);
    expect(screen.getByText('Review Queue')).toBeInTheDocument();
    expect(screen.getByText('code-reviewer')).toBeInTheDocument();
  });

  it('shows empty state when no items in queue', () => {
    setupStore({ reviewerProfile: 'code-reviewer' });
    render(<ReviewQueueSection />);
    fireEvent.click(screen.getByRole('button', { name: /expand review queue/i }));
    expect(screen.getByText('No issues in review queue')).toBeInTheDocument();
  });

  it('renders issues awaiting review', () => {
    mockIssues = [
      { identifier: 'ENG-1', title: 'Fix login bug', state: 'Done' },
      { identifier: 'ENG-2', title: 'Add tests', state: 'In Progress' },
    ];
    setupStore({
      reviewerProfile: 'code-reviewer',
      completionState: 'Done',
      running: [],
    });

    render(<ReviewQueueSection />);
    fireEvent.click(screen.getByRole('button', { name: /expand review queue/i }));

    // ENG-1 is in Done state (matches completionState) and not being reviewed
    expect(screen.getByText('ENG-1')).toBeInTheDocument();
    expect(screen.getByText('Fix login bug')).toBeInTheDocument();
    // ENG-2 is In Progress, should not appear as awaiting review
    expect(screen.queryByText('ENG-2')).toBeNull();
  });

  it('renders review button for awaiting issues and triggers review on click', () => {
    mockIssues = [{ identifier: 'ENG-1', title: 'Fix login bug', state: 'Done' }];
    setupStore({
      reviewerProfile: 'code-reviewer',
      completionState: 'Done',
    });

    render(<ReviewQueueSection />);
    fireEvent.click(screen.getByRole('button', { name: /expand review queue/i }));

    const reviewBtn = screen.getByRole('button', { name: /Review/ });
    fireEvent.click(reviewBtn);
    expect(mockMutate).toHaveBeenCalledWith('ENG-1');
  });

  it('renders issues currently being reviewed', () => {
    mockIssues = [];
    setupStore({
      reviewerProfile: 'code-reviewer',
      completionState: 'Done',
      running: [
        {
          identifier: 'ENG-3',
          state: 'In Progress',
          turnCount: 2,
          tokens: 100,
          inputTokens: 50,
          outputTokens: 50,
          elapsedMs: 5000,
          startedAt: '2024-01-01T00:00:00Z',
          kind: 'reviewer',
        },
      ],
    });

    render(<ReviewQueueSection />);
    fireEvent.click(screen.getByRole('button', { name: /expand review queue/i }));

    expect(screen.getByText('ENG-3')).toBeInTheDocument();
    expect(screen.getByText(/Reviewing/)).toBeInTheDocument();
    expect(screen.getByText(/turn 2/)).toBeInTheDocument();
  });

  it('excludes issues already being reviewed from the awaiting list', () => {
    mockIssues = [{ identifier: 'ENG-1', title: 'Fix login bug', state: 'Done' }];
    setupStore({
      reviewerProfile: 'code-reviewer',
      completionState: 'Done',
      running: [
        {
          identifier: 'ENG-1',
          state: 'In Progress',
          turnCount: 1,
          tokens: 50,
          inputTokens: 25,
          outputTokens: 25,
          elapsedMs: 2000,
          startedAt: '2024-01-01T00:00:00Z',
          kind: 'reviewer',
        },
      ],
    });

    render(<ReviewQueueSection />);
    fireEvent.click(screen.getByRole('button', { name: /expand review queue/i }));

    // ENG-1 should appear in the reviewing section, not awaiting
    expect(screen.getByText('ENG-1')).toBeInTheDocument();
    expect(screen.getByText(/Reviewing/)).toBeInTheDocument();
    // No review button since it is already being reviewed
    expect(screen.queryByText(/▶ Review/)).toBeNull();
  });

  it('renders recent review completions from history', () => {
    setupStore({
      reviewerProfile: 'code-reviewer',
      completionState: 'Done',
      history: [
        {
          identifier: 'ENG-5',
          startedAt: '2024-01-01T00:00:00Z',
          finishedAt: '2024-01-01T00:05:00Z',
          elapsedMs: 300000,
          turnCount: 3,
          tokens: 200,
          inputTokens: 100,
          outputTokens: 100,
          status: 'succeeded',
          sessionId: 'sess-1',
          kind: 'reviewer',
        },
      ],
    });

    render(<ReviewQueueSection />);
    fireEvent.click(screen.getByRole('button', { name: /expand review queue/i }));

    expect(screen.getByText('ENG-5')).toBeInTheDocument();
    expect(screen.getByText(/Review succeeded/)).toBeInTheDocument();
  });

  it('shows correct total count badge', () => {
    mockIssues = [{ identifier: 'ENG-1', title: 'Bug fix', state: 'Done' }];
    setupStore({
      reviewerProfile: 'code-reviewer',
      completionState: 'Done',
      running: [
        {
          identifier: 'ENG-3',
          state: 'In Progress',
          turnCount: 0,
          tokens: 0,
          inputTokens: 0,
          outputTokens: 0,
          elapsedMs: 1000,
          startedAt: '2024-01-01T00:00:00Z',
          kind: 'reviewer',
        },
      ],
      history: [
        {
          identifier: 'ENG-5',
          startedAt: '2024-01-01T00:00:00Z',
          finishedAt: '2024-01-01T00:01:00Z',
          elapsedMs: 60000,
          turnCount: 1,
          tokens: 50,
          inputTokens: 25,
          outputTokens: 25,
          status: 'succeeded',
          sessionId: 'sess-1',
          kind: 'reviewer',
        },
      ],
    });

    render(<ReviewQueueSection />);

    // 1 awaiting + 1 reviewing + 1 history = 3
    expect(screen.getByText('3')).toBeInTheDocument();
  });

  it('renders collapsed by default and expands without dispatching review', () => {
    mockIssues = [{ identifier: 'ENG-1', title: 'Bug fix', state: 'Done' }];
    setupStore({
      reviewerProfile: 'code-reviewer',
      completionState: 'Done',
    });

    render(<ReviewQueueSection />);

    expect(screen.getByText('Review Queue')).toBeInTheDocument();
    expect(screen.getByText('code-reviewer')).toBeInTheDocument();
    expect(screen.queryByText('ENG-1')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /expand review queue/i }));

    expect(screen.getByRole('searchbox', { name: /search review queue/i })).toBeInTheDocument();
    expect(screen.getByText('ENG-1')).toBeInTheDocument();
    expect(mockMutate).not.toHaveBeenCalled();
  });

  it('filters review queue locally and keeps rows in a scroll-capped area', () => {
    mockIssues = Array.from({ length: 6 }, (_, i) => ({
      identifier: `ENG-${String(i + 1)}`,
      title: i === 5 ? 'Need mobile polish' : `Review item ${String(i + 1)}`,
      state: 'Done',
    }));
    setupStore({
      reviewerProfile: 'code-reviewer',
      completionState: 'Done',
    });

    render(<ReviewQueueSection />);
    fireEvent.click(screen.getByRole('button', { name: /expand review queue/i }));

    const scroll = screen.getByTestId('review-queue-scroll');
    expect(scroll.className).toContain('overflow-y-auto');
    expect(scroll.className).toContain('max-h-[260px]');
    for (const row of screen.getAllByTestId('review-queue-row')) {
      expect(row.className).toContain('min-h-[52px]');
    }
    expect(screen.getByText('ENG-6')).toBeInTheDocument();

    fireEvent.change(screen.getByRole('searchbox', { name: /search review queue/i }), {
      target: { value: 'mobile' },
    });

    expect(screen.getByText('1/6')).toBeInTheDocument();
    expect(screen.getByText('ENG-6')).toBeInTheDocument();
    expect(screen.queryByText('ENG-1')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /clear queue search/i }));
    expect(screen.getByText('ENG-1')).toBeInTheDocument();
  });
});
