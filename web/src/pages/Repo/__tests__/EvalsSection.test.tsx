// #201 — Repo Analysis → Evals: the latest persisted eval result per case
// from GET /api/v1/repo/evals, with failed assertions, the prompt digest it
// ran against, when, and the regression marker. Read-only.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import { EvalsSection } from '../evals/EvalsSection';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

const PREV = 'a'.repeat(64);
const CUR = 'b'.repeat(64);

const evals = {
  dir: '/proj/.itervox/evals',
  reports: [
    {
      file: '20261021T090000.000Z.json',
      mode: 'live',
      finishedAt: '2026-10-21T09:00:00Z',
      pass: 1,
      total: 3,
    },
  ],
  cases: [
    {
      profile: 'merge-bot',
      scenario: 'green-ci-approval',
      pass: true,
      ranAt: '2026-10-21T09:00:00Z',
      mode: 'recorded',
      file: '20261021T090000.000Z.json',
      results: 2,
    },
    {
      profile: 'reviewer',
      scenario: 'rejects-and-moves-back',
      pass: false,
      failures: [
        'forbidden phrase present in comments: /ai-approved',
        'marker phrase missing from comments: changes requested',
      ],
      promptDigest: CUR,
      digestSource: 'live',
      ranAt: '2026-10-21T09:00:00Z',
      mode: 'live',
      file: '20261021T090000.000Z.json',
      regression: true,
      previousDigest: PREV,
      previousAt: '2026-10-20T09:00:00Z',
      results: 3,
    },
    {
      profile: 'reviewer',
      scenario: 'approves-with-marker',
      pass: false,
      error: 'budget: wall-clock timeout 5m0s exceeded',
      promptDigest: CUR,
      digestSource: 'live',
      ranAt: '2026-10-21T09:00:00Z',
      mode: 'live',
      file: '20261021T090000.000Z.json',
      results: 1,
    },
  ],
};

function respond(body: unknown) {
  vi.mocked(authedFetch).mockImplementation(() =>
    Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    ),
  );
}

function row(key: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-case="${key}"]`);
  if (!el) throw new Error(`no row ${key}`);
  return el;
}

beforeEach(() => {
  vi.mocked(authedFetch).mockReset();
  respond(evals);
});

describe('EvalsSection (#201)', () => {
  it('shows each case grouped by profile with pass/fail, failures, digest and regression', async () => {
    render(<EvalsSection />);
    const reviewer = await screen.findByRole('list', { name: 'Eval results for reviewer' });
    expect(
      within(reviewer).getAllByRole('listitem', { hidden: false }).length,
    ).toBeGreaterThanOrEqual(2);
    expect(screen.getByRole('list', { name: 'Eval results for merge-bot' })).toBeInTheDocument();

    const green = row('merge-bot/green-ci-approval');
    expect(within(green).getByText('pass')).toBeInTheDocument();
    expect(green).toHaveTextContent('unknown (the recording carries no digest)');
    expect(green).not.toHaveTextContent('regression');

    const rejects = row('reviewer/rejects-and-moves-back');
    expect(within(rejects).getByText('fail')).toBeInTheDocument();
    expect(within(rejects).getByText('regression')).toBeInTheDocument();
    const failures = within(rejects).getByRole('list', {
      name: 'Failed assertions of rejects-and-moves-back',
    });
    expect(within(failures).getAllByRole('listitem')).toHaveLength(2);
    expect(failures).toHaveTextContent('forbidden phrase present in comments: /ai-approved');
    expect(rejects).toHaveTextContent(
      `Prompt version: ${CUR.slice(0, 12)} (computed by the live run)`,
    );
    expect(rejects).toHaveTextContent(`Passed on ${PREV.slice(0, 12)}`);
    expect(rejects).toHaveTextContent(`fails on ${CUR.slice(0, 12)}`);

    expect(row('reviewer/approves-with-marker')).toHaveTextContent(
      'Run: budget: wall-clock timeout 5m0s exceeded',
    );
    expect(screen.getByText(/3 cases · 2 failing · 1 regressions/)).toBeInTheDocument();
    expect(vi.mocked(authedFetch)).toHaveBeenCalledWith('/api/v1/repo/evals', expect.anything());
  });

  it('explains how to produce reports when there are none, and offers no run button', async () => {
    respond({ dir: '/proj/.itervox/evals', reports: [], cases: [] });
    render(<EvalsSection />);
    expect(await screen.findByText(/No eval reports yet/)).toBeInTheDocument();
    expect(screen.getByText('make evals')).toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('surfaces a server read error', async () => {
    respond({
      dir: '/proj/.itervox/evals',
      reports: [],
      cases: [],
      error: 'evals: read results: permission denied',
    });
    render(<EvalsSection />);
    expect(await screen.findByText('evals: read results: permission denied')).toBeInTheDocument();
  });
});
