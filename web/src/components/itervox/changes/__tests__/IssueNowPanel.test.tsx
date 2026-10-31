import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { IssueNowPanel } from '../IssueNowPanel';
import { routeFetch } from './fetchMock';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../../auth/authedFetch';
const mockFetch = vi.mocked(authedFetch);

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const liveContext = {
  identifier: 'ENG-1',
  run: {
    runId: 'run-1',
    live: true,
    profile: 'coder',
    backend: 'claude',
    currentStep: 'Wiring the diff endpoint into the router.',
    lastToolCall: { name: 'Edit', target: 'internal/server/server.go' },
    toolCalls: 12,
    turnCount: 3,
    inputTokens: 1000,
    outputTokens: 500,
    totalTokens: 1500,
    queuedSteers: 1,
    steers: [{ text: 'use gitexec', at: '2026-10-22T10:00:00Z' }],
    guardrailBlocks: 2,
    lastGuardrailKind: 'push_protected',
    pendingInput: 'Which base branch?',
  },
  changedFiles: [
    { path: 'internal/server/changes.go', status: 'added', additions: 120, deletions: 0 },
    { path: 'web/src/App.tsx', status: 'modified', additions: 3, deletions: 1 },
  ],
  handoffs: [{ title: 'Plan the endpoints', path: '.itervox/handoff/a.md' }],
};

describe('IssueNowPanel (#215)', () => {
  beforeEach(() => {
    mockFetch.mockReset();
  });

  it('shows the current step, the last tool call, counters, waits and changed files', async () => {
    routeFetch(mockFetch, { '/api/v1/issues/ENG-1/context': { body: liveContext } });
    render(<IssueNowPanel identifier="ENG-1" live />, { wrapper });

    expect(await screen.findByText('Now')).toBeInTheDocument();
    expect(screen.getByText('Wiring the diff endpoint into the router.')).toBeInTheDocument();
    const tool = screen.getByTestId('now-last-tool');
    expect(tool).toHaveTextContent('Edit');
    expect(tool).toHaveTextContent('internal/server/server.go');
    expect(tool).toHaveTextContent('12 calls');
    expect(screen.getByText(/coder · claude · turn 3/)).toBeInTheDocument();
    expect(screen.getByText(/Waiting for input: Which base branch\?/)).toBeInTheDocument();
    expect(screen.getByText(/Steering: 1 delivered, 1 queued/)).toBeInTheDocument();
    expect(
      screen.getByText(/Guardrails blocked 2 operations \(last: push_protected\)/),
    ).toBeInTheDocument();
    expect(screen.getByText('internal/server/changes.go')).toBeInTheDocument();
    expect(screen.getByText('+120')).toBeInTheDocument();
    expect(screen.getByText('Plan the endpoints')).toBeInTheDocument();
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/issues/ENG-1/context', expect.anything());
  });

  it("shows the workspace's ITERVOX_PORT when it has one (#250)", async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/context': { body: { ...liveContext, port: 41003 } },
    });
    render(<IssueNowPanel identifier="ENG-1" live />, { wrapper });
    const port = await screen.findByTestId('now-port');
    expect(port).toHaveTextContent('41003');
    expect(port).toHaveTextContent('ITERVOX_PORT');
  });

  it('shows no port line without one', async () => {
    routeFetch(mockFetch, { '/api/v1/issues/ENG-1/context': { body: liveContext } });
    render(<IssueNowPanel identifier="ENG-1" live />, { wrapper });
    expect(await screen.findByText('Now')).toBeInTheDocument();
    expect(screen.queryByTestId('now-port')).not.toBeInTheDocument();
  });

  it('labels a finished run as the last run with its outcome', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/context': {
        body: { ...liveContext, run: { ...liveContext.run, live: false, outcome: 'succeeded' } },
      },
    });
    render(<IssueNowPanel identifier="ENG-1" live={false} />, { wrapper });
    expect(await screen.findByText('Last run')).toBeInTheDocument();
    expect(screen.getByText(/· succeeded/)).toBeInTheDocument();
  });

  it('renders nothing for an issue with no run and no worktree (404)', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/context': {
        status: 404,
        body: { error: { code: 'no_run_context', message: 'none' } },
      },
    });
    const { container } = render(<IssueNowPanel identifier="ENG-1" live={false} />, { wrapper });
    await vi.waitFor(() => {
      expect(mockFetch).toHaveBeenCalled();
    });
    expect(container).toBeEmptyDOMElement();
  });
});
