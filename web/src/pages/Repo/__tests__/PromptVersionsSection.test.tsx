// #200 — Repo Analysis → Prompt versions: per profile, each digest with first
// and last seen, runs, success rate and average duration; selecting one
// lists its runs from GET /api/v1/history?prompt=<digest>.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import { PromptVersionsSection } from '../prompts/PromptVersionsSection';
import { groupPromptVersionsByProfile, NO_PROFILE } from '../prompts/promptVersionGroups';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

const DIGEST_NEW = 'aaaa11112222' + '3'.repeat(52);
const DIGEST_OLD = 'bbbb44445555' + '6'.repeat(52);
const DIGEST_REV = 'cccc77778888' + '9'.repeat(52);

const versions = {
  versions: [
    {
      promptDigest: DIGEST_REV,
      runs: 1,
      statuses: { succeeded: 1 },
      successRate: 1,
      firstSeenAt: '2026-03-03T10:00:00Z',
      lastSeenAt: '2026-03-03T10:00:00Z',
      avgDurationMs: 500,
      profiles: ['reviewer'],
    },
    {
      promptDigest: DIGEST_NEW,
      runs: 4,
      statuses: { succeeded: 3, failed: 1 },
      successRate: 0.75,
      firstSeenAt: '2026-03-02T09:00:00Z',
      lastSeenAt: '2026-03-02T18:00:00Z',
      avgDurationMs: 3000,
      profiles: ['implementer'],
    },
    {
      promptDigest: DIGEST_OLD,
      runs: 2,
      statuses: { failed: 2 },
      successRate: 0,
      firstSeenAt: '2026-03-01T09:00:00Z',
      lastSeenAt: '2026-03-01T12:00:00Z',
      avgDurationMs: 65000,
      profiles: ['implementer'],
    },
  ],
  unversionedRuns: 2,
};

const runsOfNew = {
  history: [
    {
      identifier: 'ENG-1',
      startedAt: '2026-03-02T09:00:00Z',
      finishedAt: '2026-03-02T09:00:03Z',
      elapsedMs: 3000,
      turnCount: 1,
      tokens: 10,
      inputTokens: 5,
      outputTokens: 5,
      status: 'succeeded',
      promptDigest: DIGEST_NEW,
      runId: 'run-1',
    },
    {
      identifier: 'ENG-2',
      startedAt: '2026-03-02T18:00:00Z',
      finishedAt: '2026-03-02T18:00:03Z',
      elapsedMs: 3000,
      turnCount: 1,
      tokens: 10,
      inputTokens: 5,
      outputTokens: 5,
      status: 'failed',
      promptDigest: DIGEST_NEW,
      runId: 'run-2',
    },
  ],
};

function json(body: unknown): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }),
  );
}

beforeEach(() => {
  vi.mocked(authedFetch).mockReset();
  vi.mocked(authedFetch).mockImplementation((input) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    if (url === '/api/v1/history/prompt-versions') return json(versions);
    if (url.startsWith('/api/v1/history?prompt=')) return json(runsOfNew);
    return json({});
  });
});

describe('PromptVersionsSection (#200)', () => {
  it('lists each profile’s versions newest first with their stats', async () => {
    render(<PromptVersionsSection />);
    const impl = await screen.findByRole('table', { name: 'Prompt versions of implementer' });
    const rows = within(impl).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByRole('button', { name: 'aaaa11112222' })).toBeInTheDocument();
    expect(rows[0]).toHaveTextContent('75%');
    expect(rows[0]).toHaveTextContent('3s');
    expect(within(rows[0]).getAllByRole('cell')[3]).toHaveTextContent('4');
    expect(within(rows[1]).getByRole('button', { name: 'bbbb44445555' })).toBeInTheDocument();
    expect(rows[1]).toHaveTextContent('0%');
    expect(rows[1]).toHaveTextContent('1m 05s');

    const rev = screen.getByRole('table', { name: 'Prompt versions of reviewer' });
    expect(rev).toHaveTextContent('100%');
    expect(screen.getByText(/2 older runs recorded before prompt versions/)).toBeInTheDocument();
  });

  it('selecting a version filters the history by its digest', async () => {
    render(<PromptVersionsSection />);
    fireEvent.click(await screen.findByRole('button', { name: 'aaaa11112222' }));

    await waitFor(() => {
      expect(vi.mocked(authedFetch)).toHaveBeenCalledWith(
        `/api/v1/history?prompt=${DIGEST_NEW}`,
        expect.anything(),
      );
    });
    const runs = await screen.findByRole('region', { name: 'Runs of the selected prompt version' });
    expect(await within(runs).findByText('ENG-1')).toBeInTheDocument();
    expect(within(runs).getByText('ENG-2')).toBeInTheDocument();
    // Newest first.
    const items = within(runs).getAllByRole('listitem');
    expect(items[0]).toHaveTextContent('ENG-2');
    expect(screen.getByRole('button', { name: 'aaaa11112222' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );

    fireEvent.click(within(runs).getByRole('button', { name: 'Clear filter' }));
    expect(
      screen.queryByRole('region', { name: 'Runs of the selected prompt version' }),
    ).not.toBeInTheDocument();
  });

  it('shows an empty state when no run recorded a prompt version', async () => {
    vi.mocked(authedFetch).mockImplementation(() => json({ versions: [], unversionedRuns: 0 }));
    render(<PromptVersionsSection />);
    expect(await screen.findByText(/no prompt versions yet/i)).toBeInTheDocument();
  });

  it('groups versions by profile, unprofiled last', () => {
    const groups = groupPromptVersionsByProfile([
      { ...versions.versions[0], profiles: null },
      { ...versions.versions[1], profiles: ['b', 'a'] },
    ]);
    expect(groups.map((g) => g.profile)).toEqual(['a', 'b', NO_PROFILE]);
  });
});
