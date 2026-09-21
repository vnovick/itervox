import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { OutboxList } from '../OutboxList';
import type { OutboxEntryRow } from '../../../../types/schemas';

// Mutation hooks are mocked (mirrors DepsGraph.test.tsx's DepsOverridePanel
// coverage) so this file asserts the panel wires the right button to the
// right hook without hitting authedFetch — that transport contract is
// covered by web/src/queries/__tests__/outbox.test.tsx instead.
const { retryMutateSpy, discardMutateSpy, retryState, discardState } = vi.hoisted(() => ({
  retryMutateSpy: vi.fn(),
  discardMutateSpy: vi.fn(),
  retryState: { isPending: false },
  discardState: { isPending: false },
}));
vi.mock('../../../../queries/outbox', () => ({
  useRetryOutboxEntry: () => ({ mutate: retryMutateSpy, isPending: retryState.isPending }),
  useDiscardOutboxEntry: () => ({ mutate: discardMutateSpy, isPending: discardState.isPending }),
}));

function withQueryClient(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>;
}

function row(overrides: Partial<OutboxEntryRow> = {}): OutboxEntryRow {
  return {
    id: overrides.id ?? 'entry-1',
    kind: overrides.kind ?? 'update_state',
    identifier: overrides.identifier ?? 'ENG-1',
    targetState: overrides.targetState ?? 'Done',
    attempts: overrides.attempts ?? 0,
    lastError: overrides.lastError,
    degraded: overrides.degraded,
    enqueuedAt: overrides.enqueuedAt ?? '2026-05-20T10:00:00.000Z',
    nextAttemptAt: overrides.nextAttemptAt ?? '2026-05-20T10:00:00.000Z',
    rateLimitedUntil: overrides.rateLimitedUntil,
  };
}

afterEach(() => {
  vi.useRealTimers();
});

beforeEach(() => {
  retryMutateSpy.mockReset();
  discardMutateSpy.mockReset();
  retryState.isPending = false;
  discardState.isPending = false;
});

describe('OutboxList', () => {
  it('renders nothing when there are no entries', () => {
    const { container } = render(
      withQueryClient(<OutboxList entries={[]} onSelectIssue={vi.fn()} />),
    );
    expect(container).toBeEmptyDOMElement();
  });

  it('renders a row per entry', () => {
    render(
      withQueryClient(
        <OutboxList
          entries={[row({ id: 'e1', identifier: 'ENG-1' }), row({ id: 'e2', identifier: 'ENG-2' })]}
          onSelectIssue={vi.fn()}
        />,
      ),
    );
    expect(screen.getByTestId('outbox-card-e1')).toBeInTheDocument();
    expect(screen.getByTestId('outbox-card-e2')).toBeInTheDocument();
  });

  it('shows the degraded badge only for degraded entries', () => {
    render(
      withQueryClient(
        <OutboxList
          entries={[row({ id: 'e1', degraded: true }), row({ id: 'e2', degraded: false })]}
          onSelectIssue={vi.fn()}
        />,
      ),
    );
    expect(screen.getByTestId('outbox-degraded-badge-e1')).toBeInTheDocument();
    expect(screen.queryByTestId('outbox-degraded-badge-e2')).not.toBeInTheDocument();
  });

  // The spec keeps the rate-limited chip "separate from the red degraded
  // badge": a stuck entry must not lose its red badge just because it is
  // also waiting out a limit, so both render together when both apply.
  // Assertions are on testids only — never on locale-formatted time text,
  // which varies with the CI machine's timezone.
  it('renders the rate-limited chip alongside the degraded badge', () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-09-16T11:00:00Z'));
    render(
      withQueryClient(
        <OutboxList
          entries={[
            row({
              id: 'e1',
              kind: 'create_comment',
              identifier: 'ENG-1',
              attempts: 5,
              enqueuedAt: '2026-09-16T10:00:00Z',
              nextAttemptAt: '2026-09-16T12:00:05Z',
              rateLimitedUntil: '2026-09-16T12:00:00Z',
              degraded: true,
            }),
          ]}
          onSelectIssue={vi.fn()}
        />,
      ),
    );

    expect(screen.getByTestId('outbox-rate-limited-badge-e1')).toBeInTheDocument();
    expect(screen.getByTestId('outbox-degraded-badge-e1')).toBeInTheDocument();
  });

  it('shows no rate-limited chip once rateLimitedUntil is in the past', () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-09-16T13:00:00Z'));
    render(
      withQueryClient(
        <OutboxList
          entries={[row({ id: 'e1', rateLimitedUntil: '2026-09-16T12:00:00Z' })]}
          onSelectIssue={vi.fn()}
        />,
      ),
    );

    expect(screen.getByTestId('outbox-card-e1')).toBeInTheDocument();
    expect(screen.queryByTestId('outbox-rate-limited-badge-e1')).not.toBeInTheDocument();
  });

  it('shows no rate-limited chip for an unparseable rateLimitedUntil', () => {
    render(
      withQueryClient(
        <OutboxList
          entries={[row({ id: 'e1', rateLimitedUntil: 'not-a-date' })]}
          onSelectIssue={vi.fn()}
        />,
      ),
    );

    expect(screen.getByTestId('outbox-card-e1')).toBeInTheDocument();
    expect(screen.queryByTestId('outbox-rate-limited-badge-e1')).not.toBeInTheDocument();
  });

  it('calls onSelectIssue when the identifier button is clicked', () => {
    const onSelectIssue = vi.fn();
    render(
      withQueryClient(
        <OutboxList entries={[row({ identifier: 'ENG-9' })]} onSelectIssue={onSelectIssue} />,
      ),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Open issue ENG-9' }));
    expect(onSelectIssue).toHaveBeenCalledWith('ENG-9');
  });

  // The mutation-catcher scenario from the plan: retry must call
  // useRetryOutboxEntry, not useDiscardOutboxEntry, and vice versa. This
  // test fails if the Retry button were mistakenly wired to the discard
  // mutation (or dropped its handler).
  it('Retry button calls useRetryOutboxEntry with the entry id (not discard)', () => {
    render(
      withQueryClient(<OutboxList entries={[row({ id: 'entry-42' })]} onSelectIssue={vi.fn()} />),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(retryMutateSpy).toHaveBeenCalledWith('entry-42');
    expect(discardMutateSpy).not.toHaveBeenCalled();
  });

  it('Discard button calls useDiscardOutboxEntry with the entry id (not retry)', () => {
    render(
      withQueryClient(<OutboxList entries={[row({ id: 'entry-42' })]} onSelectIssue={vi.fn()} />),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(discardMutateSpy).toHaveBeenCalledWith('entry-42');
    expect(retryMutateSpy).not.toHaveBeenCalled();
  });

  it('filters rows by the search box', () => {
    render(
      withQueryClient(
        <OutboxList
          entries={[row({ id: 'e1', identifier: 'ENG-1' }), row({ id: 'e2', identifier: 'ENG-2' })]}
          onSelectIssue={vi.fn()}
        />,
      ),
    );
    fireEvent.change(screen.getByTestId('outbox-search'), { target: { value: 'ENG-2' } });
    expect(screen.queryByTestId('outbox-card-e1')).not.toBeInTheDocument();
    expect(screen.getByTestId('outbox-card-e2')).toBeInTheDocument();
  });
});
