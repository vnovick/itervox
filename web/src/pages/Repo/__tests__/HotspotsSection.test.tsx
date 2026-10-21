// #201 — Repo Analysis → Hotspots: files and directories ranked by problem
// runs, with per-signal counts, the issues involved and links to their runs.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, within } from '@testing-library/react';
import { authedFetch } from '../../../auth/authedFetch';
import { render } from '../../../test/render';
import { HotspotsSection } from '../hotspots/HotspotsSection';
import { HOTSPOTS_REFRESHING_POLL_MS } from '../../../queries/repo';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

const run = (identifier: string, signals: string[], extra: Record<string, unknown> = {}) => ({
  runId: `run-${identifier}`,
  identifier,
  status: 'failed',
  kind: 'worker',
  finishedAt: '2026-10-21T09:00:00Z',
  signals,
  ...extra,
});

const hotspots = {
  generatedAt: '2026-10-21T09:05:00Z',
  refreshing: false,
  repo: '/proj',
  baseRef: 'refs/remotes/origin/main',
  runsConsidered: 12,
  runsWithSignals: 4,
  runsAttributed: 3,
  runsUnattributed: 1,
  files: [
    {
      path: 'internal/a/x.go',
      failed: 1,
      stalled: 0,
      changeRequests: 1,
      inputRequests: 0,
      reverted: 0,
      total: 2,
      issues: ['ENG-1', 'ENG-2'],
      runs: [
        run('ENG-2', ['change_request'], { kind: 'reviewer', status: 'succeeded' }),
        run('ENG-1', ['failed'], { prUrl: 'https://github.com/acme/app/pull/7' }),
      ],
    },
    {
      path: 'docs/guide.md',
      failed: 0,
      stalled: 0,
      changeRequests: 0,
      inputRequests: 0,
      reverted: 1,
      total: 1,
      issues: ['ENG-3'],
      runs: [run('ENG-3', ['reverted', 'some_future_signal'])],
    },
  ],
  directories: [
    {
      path: 'internal/',
      failed: 1,
      stalled: 0,
      changeRequests: 1,
      inputRequests: 0,
      reverted: 0,
      total: 2,
      issues: ['ENG-1', 'ENG-2'],
      runs: [],
    },
  ],
  notes: ['1 run(s) that went wrong could not be attributed to files.'],
};

function respond(...bodies: unknown[]) {
  const queue = [...bodies];
  vi.mocked(authedFetch).mockImplementation(() => {
    const body = queue.length > 1 ? queue.shift() : queue[0];
    return Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  });
}

function row(path: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-path="${path}"]`);
  if (!el) throw new Error(`no row ${path}`);
  return el;
}

beforeEach(() => {
  vi.mocked(authedFetch).mockReset();
  respond(hotspots);
});

describe('HotspotsSection (#201)', () => {
  it('ranks files with per-signal counts, issues and links to their runs', async () => {
    render(<HotspotsSection />, { route: '/repo#hotspots' });
    const list = await screen.findByRole('list', { name: 'File hotspots' });
    const items = within(list)
      .getAllByRole('listitem')
      .filter((li) => li.dataset.path);
    expect(items.map((li) => li.dataset.path)).toEqual(['internal/a/x.go', 'docs/guide.md']);

    const x = row('internal/a/x.go');
    expect(x).toHaveTextContent('2 problems');
    expect(within(x).getByText('1 failed')).toBeInTheDocument();
    expect(within(x).getByText('1 change requested')).toBeInTheDocument();
    expect(within(x).queryByText(/stalled/)).not.toBeInTheDocument();
    const issueLinks = within(x).getAllByRole('link', { name: 'ENG-1' });
    expect(issueLinks[0]).toHaveAttribute('href', '/logs/ENG-1');
    const runs = within(x).getByRole('list', { name: 'Runs that touched internal/a/x.go' });
    expect(within(runs).getAllByRole('listitem')).toHaveLength(2);
    expect(runs).toHaveTextContent('change requested · reviewer');
    expect(within(runs).getByRole('link', { name: 'PR' })).toHaveAttribute(
      'href',
      'https://github.com/acme/app/pull/7',
    );

    // An unknown signal from a newer server is dropped, not mislabelled.
    const guide = row('docs/guide.md');
    expect(within(guide).getByText('1 PR reverted')).toBeInTheDocument();
    expect(guide).not.toHaveTextContent('some_future_signal');

    expect(screen.getByText(/4 of 12 runs went wrong; 3 attributed to files/)).toBeInTheDocument();
    expect(screen.getByRole('list', { name: 'Hotspot notes' })).toHaveTextContent(
      'could not be attributed',
    );
    expect(screen.getByText(/against refs\/remotes\/origin\/main/)).toBeInTheDocument();
    expect(vi.mocked(authedFetch)).toHaveBeenCalledWith('/api/v1/repo/hotspots', expect.anything());
  });

  it('switches to directories', async () => {
    render(<HotspotsSection />, { route: '/repo#hotspots' });
    await screen.findByRole('list', { name: 'File hotspots' });
    fireEvent.click(screen.getByRole('button', { name: 'Directories' }));
    expect(screen.getByRole('button', { name: 'Directories' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    const list = screen.getByRole('list', { name: 'Directory hotspots' });
    expect(within(list).getByText('internal/')).toBeInTheDocument();
  });

  it('shows the first computation in progress and polls until it lands', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      respond(
        {
          refreshing: true,
          runsConsidered: 0,
          runsWithSignals: 0,
          runsAttributed: 0,
          runsUnattributed: 0,
          files: [],
          directories: [],
        },
        hotspots,
      );
      render(<HotspotsSection />, { route: '/repo#hotspots' });
      expect(await screen.findByText(/Computing hotspots/)).toBeInTheDocument();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(HOTSPOTS_REFRESHING_POLL_MS + 10);
      });
      expect(await screen.findByRole('list', { name: 'File hotspots' })).toBeInTheDocument();
      expect(vi.mocked(authedFetch).mock.calls.length).toBeGreaterThanOrEqual(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it('says so when nothing went wrong', async () => {
    respond({ ...hotspots, files: [], directories: [], notes: [] });
    render(<HotspotsSection />, { route: '/repo#hotspots' });
    expect(await screen.findByText(/No hotspots/)).toBeInTheDocument();
  });
});
