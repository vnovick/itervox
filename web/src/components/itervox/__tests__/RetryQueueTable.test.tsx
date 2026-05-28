import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import RetryQueueTable from '../RetryQueueTable';
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
});
