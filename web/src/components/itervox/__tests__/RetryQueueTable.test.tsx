import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import RetryQueueTable from '../RetryQueueTable';
import { axeViolations } from '../../../test/axe';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeRetryRow, makeSnapshot } from '../../../test/fixtures/snapshots';

const mutate = vi.fn();

vi.mock('../../../queries/issues', () => ({
  useCancelIssue: () => ({ mutate }),
}));

describe('RetryQueueTable', () => {
  beforeEach(() => {
    mutate.mockReset();
    useItervoxStore.setState({ snapshot: makeSnapshot() });
  });

  // CORE-073 — same verb as the detail slide's retrying footer.
  it('labels the row action Cancel retry', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ retrying: [makeRetryRow({ identifier: 'ENG-RETRY' })] }),
    });
    render(<RetryQueueTable />);
    fireEvent.click(screen.getByRole('button', { name: '✕ Cancel retry' }));
    expect(mutate).toHaveBeenCalledWith('ENG-RETRY', expect.anything());
  });

  it('renders a local search control for retry queue content', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ retrying: [makeRetryRow({ identifier: 'ENG-RETRY' })] }),
    });

    render(<RetryQueueTable />);

    expect(screen.getByRole('searchbox', { name: /search retry queue/i })).toBeInTheDocument();
    expect(screen.getByText('ENG-RETRY')).toBeInTheDocument();
  });

  it('filters by identifier and error without mutating retry state', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        retrying: [
          makeRetryRow({ identifier: 'ENG-RATE', error: 'rate limit exceeded' }),
          makeRetryRow({ identifier: 'ENG-BUILD', error: 'build failed' }),
        ],
      }),
    });

    render(<RetryQueueTable />);

    fireEvent.change(screen.getByRole('searchbox', { name: /search retry queue/i }), {
      target: { value: 'rate limit' },
    });

    expect(screen.getByText('ENG-RATE')).toBeInTheDocument();
    expect(screen.queryByText('ENG-BUILD')).not.toBeInTheDocument();
    expect(mutate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: /clear queue search/i }));
    expect(screen.getByText('ENG-BUILD')).toBeInTheDocument();
  });

  it('uses distinct copy for an empty search result', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ retrying: [makeRetryRow({ identifier: 'ENG-RETRY' })] }),
    });

    render(<RetryQueueTable />);

    fireEvent.change(screen.getByRole('searchbox', { name: /search retry queue/i }), {
      target: { value: 'missing' },
    });

    expect(screen.getByText('No matching retry queue items')).toBeInTheDocument();
  });

  // M5-close (axe nested-interactive) — the row toggle is a real button that
  // does not contain the identifier link or the Cancel retry action.
  it('rows have no nested interactive controls', async () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ retrying: [makeRetryRow({ identifier: 'ENG-RETRY' })] }),
    });
    const { container } = render(<RetryQueueTable />);
    expect(await axeViolations(container)).toEqual([]);
    const toggle = screen.getByRole('button', {
      name: 'Toggle details for retrying issue ENG-RETRY',
    });
    expect(toggle.tagName).toBe('BUTTON');
    // Expanding renders the session log accordion (needs the query client),
    // covered in RunningSessionsTable; here only the collapsed state.
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
  });
});
