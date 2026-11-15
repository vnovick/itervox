import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { IssueActivity } from '../IssueActivity';
import { routeFetch } from '../../changes/__tests__/fetchMock';
import { report } from './fixtures';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../../auth/authedFetch';
const mockFetch = vi.mocked(authedFetch);

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

describe('IssueActivity (system visibility)', () => {
  beforeEach(() => {
    mockFetch.mockReset();
    useItervoxStore.setState({ snapshot: makeSnapshot({ callerScope: 'operator' }) });
  });

  it('loads on open, filters, switches kinds and flags writes outside the workspace', async () => {
    routeFetch(mockFetch, { '/api/v1/issues/ENG-1/activity': { body: report } });
    const user = userEvent.setup();
    render(<IssueActivity identifier="ENG-1" live={false} />, { wrapper });
    expect(mockFetch).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: /Activity/ }));
    expect(await screen.findByTestId('activity-mode')).toHaveTextContent('sampled');
    expect(screen.getByTestId('activity-outside')).toHaveTextContent(
      '1 write outside the workspace',
    );
    const commands = screen.getByRole('table', { name: 'Commands' });
    expect(within(commands).getByText(/status --short/)).toBeInTheDocument();
    expect(within(commands).getByText('3')).toBeInTheDocument();
    expect(screen.getByTestId('activity-notes')).toHaveTextContent('may be missed');

    await user.type(screen.getByRole('searchbox', { name: 'Filter activity' }), 'test');
    expect(
      within(screen.getByRole('table', { name: 'Commands' })).queryByText(/status/),
    ).toBeNull();
    await user.clear(screen.getByRole('searchbox', { name: 'Filter activity' }));

    await user.click(screen.getByRole('button', { name: /^Files/ }));
    const files = screen.getByRole('table', { name: 'Files' });
    expect(within(files).getByText('/home/me/.npmrc')).toBeInTheDocument();
    expect(within(files).getByText('outside')).toBeInTheDocument();
    expect(screen.getByText(/4 more distinct files were not kept/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /^Connections/ }));
    expect(screen.getByText('140.82.112.3:443')).toBeInTheDocument();

    // A past run.
    await user.selectOptions(screen.getByRole('combobox', { name: 'Run' }), 'run-0');
    expect(mockFetch).toHaveBeenCalledWith(
      '/api/v1/issues/ENG-1/activity?run=run-0',
      expect.anything(),
    );
  });

  it('says what the platform cannot record', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/activity': {
        body: {
          ...report,
          platform: 'darwin',
          supports: { commands: true, files: false, connections: true },
          notes: [
            'File activity is not available on macOS: it needs the Endpoint Security entitlement.',
          ],
          files: [],
        },
      },
    });
    const user = userEvent.setup();
    render(<IssueActivity identifier="ENG-1" live={false} defaultOpen />, { wrapper });
    await screen.findByTestId('activity-mode');
    await user.click(screen.getByRole('button', { name: /^Files/ }));
    expect(screen.getByTestId('activity-unsupported')).toHaveTextContent('Endpoint Security');
  });

  it('asks a viewer for an operator token and never fetches', async () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ callerScope: 'viewer' }) });
    const user = userEvent.setup();
    render(<IssueActivity identifier="ENG-1" live />, { wrapper });
    await user.click(screen.getByRole('button', { name: /Activity/ }));
    expect(screen.getByText(/needs an operator token/)).toBeInTheDocument();
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it('shows the empty and error states', async () => {
    routeFetch(mockFetch, { '/api/v1/issues/ENG-1/activity': { status: 404, body: {} } });
    render(<IssueActivity identifier="ENG-1" live={false} defaultOpen />, { wrapper });
    expect(await screen.findByText(/No activity is recorded/)).toBeInTheDocument();
  });
});
