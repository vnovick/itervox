import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { NotificationsView } from '../NotificationsView';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeSnapshot } from '../../../test/fixtures/snapshots';
import { makeIssue } from '../../../test/fixtures/issues';

function renderView(onSelect = vi.fn()) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const utils = render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <NotificationsView onSelect={onSelect} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...utils, onSelect };
}

describe('NotificationsView', () => {
  it('renders the empty state when total === 0', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot() });
    renderView();
    expect(screen.getByText(/all caught up/i)).toBeInTheDocument();
  });

  it('renders all five group sections when populated', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        completionState: 'In Review',
        currentAppSessionId: 'sess-1',
        inputRequired: [
          {
            identifier: 'ENG-1',
            sessionId: 'r1',
            state: 'input_required',
            context: 'q',
            queuedAt: new Date().toISOString(),
          },
        ],
        retrying: [
          {
            identifier: 'ENG-2',
            attempt: 2,
            dueAt: new Date(Date.now() + 60_000).toISOString(),
          },
        ],
        paused: ['ENG-3'],
        history: [
          {
            identifier: 'ENG-4',
            startedAt: new Date(Date.now() - 60_000).toISOString(),
            finishedAt: new Date().toISOString(),
            elapsedMs: 1000,
            turnCount: 1,
            tokens: 0,
            inputTokens: 0,
            outputTokens: 0,
            status: 'succeeded',
            kind: 'worker',
            appSessionId: 'sess-1',
          },
        ],
        configInvalid: { error: 'invalid', retryAttempt: 1 },
      }),
    });
    // Need an issue in completion state for the review group to fire.
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    // Stub the issues query result by setting query data directly.
    qc.setQueryData(['issues'], [makeIssue({ identifier: 'ENG-4', state: 'In Review' })]);
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <NotificationsView onSelect={vi.fn()} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    // Section headings use h2 with text "<Label> · N". Use role+name to
    // disambiguate from row meta text ("paused", "attempt 2", etc.) that
    // happens to share substrings.
    expect(screen.getByRole('heading', { level: 2, name: /needs input · 1/i })).toBeInTheDocument();
    expect(
      screen.getByRole('heading', { level: 2, name: /ready for review · 1/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2, name: /retrying · 1/i })).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2, name: /paused · 1/i })).toBeInTheDocument();
    expect(
      screen.getByRole('heading', { level: 2, name: /config issues · 1/i }),
    ).toBeInTheDocument();
  });

  it('row click with select-issue clickAction calls onSelect with the identifier', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        inputRequired: [
          {
            identifier: 'ENG-9',
            sessionId: 's',
            state: 'input_required',
            context: '?',
            queuedAt: new Date().toISOString(),
          },
        ],
      }),
    });
    const { onSelect } = renderView();
    fireEvent.click(screen.getByText('ENG-9'));
    expect(onSelect).toHaveBeenCalledWith('ENG-9');
  });

  it('omits empty groups (no header for groups with zero items)', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        retrying: [
          {
            identifier: 'ENG-2',
            attempt: 1,
            dueAt: new Date(Date.now() + 60_000).toISOString(),
          },
        ],
      }),
    });
    renderView();
    expect(screen.getByRole('heading', { level: 2, name: /retrying · 1/i })).toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { level: 2, name: /needs input/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { level: 2, name: /ready for review/i }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { level: 2, name: /^paused/i })).not.toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { level: 2, name: /config issues/i }),
    ).not.toBeInTheDocument();
  });
});
