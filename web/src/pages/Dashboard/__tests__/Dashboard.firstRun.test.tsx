// CORE-097 — first-run checklist on an empty dashboard.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Dashboard from '../index';
import { render } from '../../../test/render';
import { authedFetch } from '../../../auth/authedFetch';
import { makeHistoryRow, makeSnapshot } from '../../../test/fixtures/snapshots';
import { makeIssue } from '../../../test/fixtures/issues';
import { useUIStore } from '../../../store/uiStore';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
vi.mock('../../../components/itervox/NarrativeFeed', () => ({ NarrativeFeed: () => null }));
const mockFetch = vi.mocked(authedFetch);

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** issues: the /api/v1/issues answer ('pending' never settles, 'error' is a 500). */
function routeIssues(issues: unknown[] | 'pending' | 'error') {
  let settle: ((r: Response) => void) | null = null;
  mockFetch.mockImplementation((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    if (url.endsWith('/api/v1/issues')) {
      if (issues === 'pending') {
        return new Promise<Response>((resolve) => {
          settle = resolve;
        });
      }
      if (issues === 'error') return Promise.resolve(json({ error: { message: 'down' } }, 500));
      return Promise.resolve(json(issues));
    }
    return Promise.resolve(json([]));
  });
  return { resolve: (list: unknown[]) => settle?.(json(list)) };
}

const emptySnapshot = makeSnapshot({
  trackerKind: 'linear',
  activeStates: ['Todo', 'In Progress'],
  availableProfiles: ['default'],
});

beforeEach(() => {
  mockFetch.mockReset();
  try {
    localStorage.removeItem('itervox.firstRun.dismissed');
  } catch {
    // ignore
  }
});

describe('Dashboard', () => {
  it('shows first-run checklist when no issues and no history', async () => {
    routeIssues([]);
    render(<Dashboard />, { snapshot: emptySnapshot });
    const list = await screen.findByRole('region', { name: /getting started/i });
    expect(within(list).getByText(/no dispatch this session/i)).toBeInTheDocument();
    expect(within(list).getByText(/all projects/i)).toBeInTheDocument();
  });

  it('checklist marks tracker as configured-not-verified while the issues query is pending, verified after it succeeds with an empty list, and error after it rejects', async () => {
    const pending = routeIssues('pending');
    const { unmount } = render(<Dashboard />, { snapshot: emptySnapshot });
    const tracker = () => screen.getByTestId('first-run-tracker');
    await waitFor(() => {
      expect(tracker()).toHaveAttribute('data-state', 'configured');
    });
    await act(async () => {
      pending.resolve([]);
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(tracker()).toHaveAttribute('data-state', 'verified');
    });
    unmount();
    routeIssues('error');
    render(<Dashboard />, { snapshot: emptySnapshot });
    await waitFor(
      () => {
        expect(screen.getByTestId('first-run-tracker')).toHaveAttribute('data-state', 'error');
      },
      { timeout: 4000 },
    );
  });

  it('checklist hides once issues exist', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    render(<Dashboard />, { snapshot: emptySnapshot });
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalled();
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(screen.queryByRole('region', { name: /getting started/i })).not.toBeInTheDocument();
  });

  it('checklist does not render when issues exist but the state filter hides them', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1', state: 'Todo' })]);
    render(<Dashboard />, { snapshot: emptySnapshot });
    act(() => {
      useUIStore.setState({ dashboardStateFilter: 'done', dashboardSearch: 'zzz-nothing' });
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(screen.queryByRole('region', { name: /getting started/i })).not.toBeInTheDocument();
  });

  it('does not show when history has a run, and can be dismissed', async () => {
    routeIssues([]);
    const user = userEvent.setup();
    const { unmount } = render(<Dashboard />, {
      snapshot: makeSnapshot({ ...emptySnapshot, history: [makeHistoryRow()] }),
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(screen.queryByRole('region', { name: /getting started/i })).not.toBeInTheDocument();
    unmount();
    render(<Dashboard />, { snapshot: emptySnapshot });
    const list = await screen.findByRole('region', { name: /getting started/i });
    await user.click(within(list).getByRole('button', { name: /dismiss/i }));
    expect(screen.queryByRole('region', { name: /getting started/i })).not.toBeInTheDocument();
  });
});
