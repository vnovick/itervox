// #218 — the decision timeline: every run's decisions.tsv, oldest run
// first, each decision with its why and the alternative considered.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import { DecisionsTimeline } from '../DecisionsTimeline';
import { routeFetch } from '../changes/__tests__/fetchMock';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

const decisions = {
  identifier: 'ENG-1',
  runs: [
    {
      path: '.itervox/handoff/2026-05-01T09-00-00.000Z_researcher.decisions.tsv',
      runTimestamp: '2026-05-01T09-00-00.000Z',
      role: 'researcher',
      decisions: [
        {
          time: '2026-05-01T09:05:00Z',
          at: '2026-05-01T09:05:00Z',
          decision: 'Reproduce first',
          why: 'the report had no steps',
          alternative: 'patch blind',
        },
      ],
    },
    {
      path: '.itervox/handoff/2026-05-02T10-00-00.000Z_implementer.decisions.tsv',
      runTimestamp: '2026-05-02T10-00-00.000Z',
      role: 'implementer',
      truncated: true,
      decisions: [{ time: 'soon', decision: 'Keep the cap at 64 KiB' }],
    },
    { path: '.itervox/handoff/empty.decisions.tsv', decisions: [] },
  ],
};

beforeEach(() => {
  mockFetch.mockReset();
});

describe('DecisionsTimeline (#218)', () => {
  it('renders each run with its decisions in order', async () => {
    routeFetch(mockFetch, { '/api/v1/issues/ENG-1/decisions': { body: decisions } });
    render(<DecisionsTimeline identifier="ENG-1" live={false} />);
    const section = await screen.findByTestId('decisions-timeline');
    const runs = within(section)
      .getAllByRole('listitem')
      .filter((li) => li.parentElement?.parentElement === section);
    expect(runs).toHaveLength(2);
    expect(runs[0]).toHaveTextContent('researcher · 2026-05-01 09:00');
    expect(runs[0]).toHaveTextContent('09:05');
    expect(runs[0]).toHaveTextContent('Reproduce first');
    expect(runs[0]).toHaveTextContent('Why: the report had no steps');
    expect(runs[0]).toHaveTextContent('Instead of: patch blind');
    expect(runs[1]).toHaveTextContent('implementer · 2026-05-02 10:00 · truncated');
    expect(runs[1]).toHaveTextContent('soon');
    expect(runs[1]).toHaveTextContent('Keep the cap at 64 KiB');
  });

  it('renders nothing when no run logged a decision', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/decisions': { body: { identifier: 'ENG-1', runs: [] } },
    });
    render(<DecisionsTimeline identifier="ENG-1" live />);
    await waitFor(() => {
      expect(mockFetch).toHaveBeenCalledWith('/api/v1/issues/ENG-1/decisions', expect.anything());
    });
    expect(screen.queryByTestId('decisions-timeline')).not.toBeInTheDocument();
  });
});
