// Cost and outcome visibility — Repo Analysis → Outcomes: per profile and
// per playbook figures from GET /api/v1/outcomes, 7- or 30-day window.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import OutcomesSection from '../outcomes/OutcomesSection';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

function group(name: string, over: Record<string, number> = {}) {
  return {
    name,
    runs: 4,
    finished: 4,
    succeeded: 3,
    successRate: 0.75,
    retries: 1,
    retryRate: 0.25,
    prs: 2,
    prsMerged: 1,
    prsClosed: 0,
    prsReverted: 1,
    prsOpen: 0,
    medianElapsedMs: 90_000,
    medianTokens: 1500,
    totalTokens: 6000,
    cacheReadTokens: 0,
    ...over,
  };
}

function response(window: string, over: Record<string, unknown> = {}) {
  return {
    window,
    windowStart: '2026-10-23T00:00:00Z',
    generatedAt: '2026-11-22T00:00:00Z',
    historyRuns: 6,
    earliestRunAt: '2026-11-01T00:00:00Z',
    total: group('', { runs: 6, prs: 2, prsMerged: 1 }),
    profiles: [group('implementer'), group('', { runs: 2, prs: 0, finished: 0 })],
    playbooks: [group('bugfix')],
    ...over,
  };
}

const mockFetch = vi.mocked(authedFetch);

beforeEach(() => {
  mockFetch.mockReset();
  mockFetch.mockImplementation((url) => {
    const w =
      new URL(typeof url === 'string' ? url : '', 'http://x').searchParams.get('window') ?? '30d';
    return Promise.resolve(new Response(JSON.stringify(response(w)), { status: 200 }));
  });
});

describe('OutcomesSection', () => {
  it('shows the profile and playbook tables for 30 days by default', async () => {
    render(<OutcomesSection />);
    const profiles = await screen.findByRole('table', { name: 'Outcomes by Profile' });
    expect(mockFetch).toHaveBeenCalledWith('/api/v1/outcomes?window=30d', expect.anything());
    const rows = within(profiles).getAllByRole('row');
    expect(rows[1]).toHaveTextContent('implementer');
    expect(rows[1]).toHaveTextContent('75%');
    expect(rows[1]).toHaveTextContent('1 / 2 (1 reverted)');
    expect(rows[1]).toHaveTextContent('25%');
    expect(rows[2]).toHaveTextContent('(default)');
    expect(rows[2]).toHaveTextContent('—');
    expect(screen.getByRole('table', { name: 'Outcomes by Playbook' })).toHaveTextContent('bugfix');
    expect(screen.getByText(/6 runs in the last 30 days, 1 of 2 PRs merged/)).toBeInTheDocument();
    expect(screen.getByText(/Run history is bounded/)).toBeInTheDocument();
  });

  it('switches to the 7-day window', async () => {
    render(<OutcomesSection />);
    await screen.findByRole('table', { name: 'Outcomes by Profile' });
    fireEvent.click(screen.getByRole('button', { name: '7 days' }));
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith('/api/v1/outcomes?window=7d', expect.anything());
    });
    expect(screen.getByRole('button', { name: '7 days' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('says so when the window has no runs, and when the fetch fails', async () => {
    mockFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify(
          response('30d', {
            profiles: null,
            playbooks: [],
            earliestRunAt: undefined,
            total: group('', { runs: 0, prs: 0 }),
          }),
        ),
        { status: 200 },
      ),
    );
    const { unmount } = render(<OutcomesSection />);
    expect(await screen.findAllByText('No runs in this window.')).toHaveLength(2);
    unmount();

    mockFetch.mockResolvedValue(new Response('{}', { status: 500 }));
    render(<OutcomesSection />);
    expect(await screen.findByText('Could not load the outcomes.')).toBeInTheDocument();
  });
});
