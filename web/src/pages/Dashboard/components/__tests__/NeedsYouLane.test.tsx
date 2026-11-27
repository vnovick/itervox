// The "Needs you" lane: what waits on a person, oldest first as the daemon
// sends it, with the live wait and the existing answer paths. Real mutation
// hooks; only the transport (authedFetch) is mocked, so the tests prove the
// lane reuses provide-input ("approve" / "reject" / a reply).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../../auth/authedFetch';
import { NeedsYouLane } from '../NeedsYouLane';
import { render } from '../../../../test/render';
import { axeViolations } from '../../../../test/axe';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import type { NeedsYouRow, StateSnapshot } from '../../../../types/schemas';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

const NOW = Date.parse('2026-10-22T12:00:00Z');

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  return input instanceof URL ? input.href : input.url;
}

function rows(): NeedsYouRow[] {
  return [
    {
      kind: 'question',
      identifier: 'ENG-12',
      title: 'Pick a database',
      summary: 'Which database should I use?',
      waitingSince: '2026-10-22T09:46:00Z',
    },
    {
      kind: 'pipeline-change',
      identifier: 'ENG-7',
      summary: 'Pipeline change: the agent wants to push changes to `ci.yml`',
      waitingSince: '2026-10-22T11:20:00Z',
    },
    {
      kind: 'approval',
      identifier: 'ENG-3',
      summary: 'Agent wants to run: `npm publish`',
      waitingSince: '2026-10-22T11:59:30Z',
    },
  ];
}

function snap(extra: Partial<StateSnapshot> = {}): StateSnapshot {
  return {
    ...makeSnapshot({
      inputRequired: [
        {
          identifier: 'ENG-7',
          sessionId: 's7',
          state: 'input_required',
          context: 'Pipeline change',
          queuedAt: '2026-10-22T11:20:00Z',
          guardrailApproval: true,
          guardrailLanePaths: ['.github/workflows/ci.yml'],
          guardrailLanePathsTotal: 1,
        },
      ],
    }),
    needsYou: rows(),
    ...extra,
  };
}

function bodyOf(call: Parameters<typeof authedFetch> | undefined): unknown {
  const body = call?.[1]?.body;
  return typeof body === 'string' ? JSON.parse(body) : undefined;
}

function provideCalls(identifier: string) {
  return mockFetch.mock.calls.filter(
    ([u]) => urlOf(u) === `/api/v1/issues/${identifier}/provide-input`,
  );
}

beforeEach(() => {
  mockFetch.mockReset();
  mockFetch.mockImplementation(() =>
    Promise.resolve(
      new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }),
    ),
  );
  vi.useFakeTimers({ shouldAdvanceTime: true, now: NOW });
});
afterEach(() => {
  vi.useRealTimers();
});

describe('NeedsYouLane', () => {
  it('renders nothing when nothing waits on a person', () => {
    const { container } = render(<NeedsYouLane onSelectIssue={vi.fn()} />, {
      snapshot: makeSnapshot(),
    });
    expect(container).toBeEmptyDOMElement();
  });

  it('lists every item in the daemon order with its wait, kind and line', async () => {
    const { container } = render(<NeedsYouLane onSelectIssue={vi.fn()} />, { snapshot: snap() });
    expect(screen.getByTestId('needs-you-count')).toHaveTextContent('3');
    const items = screen.getAllByTestId(/^needs-you-item-/);
    expect(items.map((li) => li.getAttribute('data-testid'))).toEqual([
      'needs-you-item-ENG-12',
      'needs-you-item-ENG-7',
      'needs-you-item-ENG-3',
    ]);
    const first = within(items[0]);
    expect(first.getByTestId('needs-you-wait')).toHaveTextContent('2h 14m');
    expect(first.getByText('Question')).toBeInTheDocument();
    expect(first.getByText('Pick a database')).toBeInTheDocument();
    expect(first.getByText('Which database should I use?')).toBeInTheDocument();
    expect(within(items[1]).getAllByText('Pipeline change')[0]).toHaveClass('pill');
    expect(within(items[1]).getByTestId('pipeline-change-notice')).toBeInTheDocument();
    expect(within(items[2]).getByTestId('needs-you-wait')).toHaveTextContent('under 1m');
    expect(await axeViolations(container)).toEqual([]);
  });

  it('moves the wait on its own between snapshot pushes', () => {
    render(<NeedsYouLane onSelectIssue={vi.fn()} />, { snapshot: snap() });
    const last = within(screen.getByTestId('needs-you-item-ENG-3'));
    expect(last.getByTestId('needs-you-wait')).toHaveTextContent('under 1m');
    act(() => {
      vi.advanceTimersByTime(2 * 60_000);
    });
    expect(last.getByTestId('needs-you-wait')).toHaveTextContent('2m');
  });

  it('approves and rejects through provide-input', async () => {
    render(<NeedsYouLane onSelectIssue={vi.fn()} />, { snapshot: snap() });
    fireEvent.click(screen.getByRole('button', { name: 'Approve ENG-3' }));
    await waitFor(() => {
      expect(provideCalls('ENG-3')).toHaveLength(1);
    });
    expect(bodyOf(provideCalls('ENG-3')[0])).toEqual({ message: 'approve' });
    fireEvent.click(screen.getByRole('button', { name: 'Reject ENG-7' }));
    await waitFor(() => {
      expect(provideCalls('ENG-7')).toHaveLength(1);
    });
    expect(bodyOf(provideCalls('ENG-7')[0])).toEqual({ message: 'reject' });
  });

  it('answers a question with the shared reply box and opens the issue', async () => {
    const onSelect = vi.fn();
    render(<NeedsYouLane onSelectIssue={onSelect} />, { snapshot: snap() });
    fireEvent.click(screen.getByRole('button', { name: 'Answer ENG-12' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Reply to the agent on ENG-12' }), {
      target: { value: 'Postgres' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Send reply to ENG-12' }));
    await waitFor(() => {
      expect(provideCalls('ENG-12')).toHaveLength(1);
    });
    expect(bodyOf(provideCalls('ENG-12')[0])).toEqual({ message: 'Postgres' });
    fireEvent.click(screen.getByRole('button', { name: 'Open ENG-3' }));
    expect(onSelect).toHaveBeenCalledWith('ENG-3');
  });

  it('shows a viewer the lane without the answer controls', () => {
    render(<NeedsYouLane onSelectIssue={vi.fn()} />, { snapshot: snap({ callerScope: 'viewer' }) });
    expect(screen.getAllByTestId(/^needs-you-item-/)).toHaveLength(3);
    expect(screen.queryByRole('button', { name: 'Approve ENG-3' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Answer ENG-12' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Open ENG-12' })).toBeInTheDocument();
  });
});
