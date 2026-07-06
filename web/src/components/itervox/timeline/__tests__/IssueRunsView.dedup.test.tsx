import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest';
import { render, screen } from '@testing-library/react';
import { IssueRunsView } from '../IssueRunsView';
import { dedupSessionsPreferLive } from '../types';
import type { IssueGroup, NormalisedSession } from '../types';

// gaps_11 G-13(b) / todolist6 codex-B6 box 1 — grouping-layer dedup for the
// Timeline. The page holds the running list via useStableValue for up to 5s
// after a run exits, so the same sessionId can reach IssueRunsView as BOTH a
// stale 'live' session and a fresh history session. Before this dedup, the
// `run.sessionId` React key collided and the run rendered twice.

const T0 = '2026-04-29T00:00:00.000Z';
const T1 = '2026-04-29T00:05:00.000Z';

function makeSession(overrides: Partial<NormalisedSession>): NormalisedSession {
  return {
    identifier: 'ENG-1',
    startedAt: T0,
    elapsedMs: 60_000,
    turnCount: 3,
    tokens: 1200,
    status: 'succeeded',
    finishedAt: T1,
    sessionId: 'sess-1',
    ...overrides,
  };
}

function makeGroup(runs: NormalisedSession[]): IssueGroup {
  return {
    identifier: 'ENG-1',
    runs,
    latestStatus: runs[runs.length - 1].status,
    latestStartedAt: runs[runs.length - 1].startedAt,
  };
}

function renderView(group: IssueGroup) {
  return render(
    <IssueRunsView
      group={group}
      logs={[]}
      viewStart={Date.parse(T0) - 60_000}
      viewEnd={Date.parse(T1) + 60_000}
      expandedRunAt={null}
      selectedSubagentIdx={null}
      onToggleExpand={vi.fn()}
      onSelectSubagent={vi.fn()}
    />,
  );
}

function duplicateKeyWarnings(spy: MockInstance): unknown[][] {
  return spy.mock.calls.filter((args) =>
    args.some((a) => typeof a === 'string' && a.toLowerCase().includes('same key')),
  );
}

describe('IssueRunsView dedup (gaps_11 G-13, codex-B6)', () => {
  let consoleError: MockInstance;

  beforeEach(() => {
    consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
  });

  afterEach(() => {
    consoleError.mockRestore();
  });

  it('renders a single row when the same sessionId appears as both history and live', () => {
    const done = makeSession({ status: 'succeeded', sessionId: 'sess-dup' });
    const live = makeSession({
      status: 'live',
      sessionId: 'sess-dup',
      finishedAt: undefined,
    });

    renderView(makeGroup([done, live]));

    // One row, not two: '#1' renders, '#2' must not.
    expect(screen.getByText('#1')).toBeInTheDocument();
    expect(screen.queryByText('#2')).toBeNull();
    // Live row preferred: the non-live status badge must not render.
    expect(screen.queryByText(/succeeded/i)).toBeNull();
    expect(duplicateKeyWarnings(consoleError)).toEqual([]);
  });

  it('renders distinct runs (different sessionIds) without deduping', () => {
    const first = makeSession({ sessionId: 'sess-a' });
    const second = makeSession({ sessionId: 'sess-b', startedAt: T1, finishedAt: undefined });

    renderView(makeGroup([first, second]));

    expect(screen.getByText('#1')).toBeInTheDocument();
    expect(screen.getByText('#2')).toBeInTheDocument();
    expect(duplicateKeyWarnings(consoleError)).toEqual([]);
  });
});

describe('dedupSessionsPreferLive (gaps_11 G-13, codex-B6)', () => {
  it('prefers the live row over a history row with the same sessionId, keeping position', () => {
    const done = makeSession({ status: 'succeeded', sessionId: 'sess-x' });
    const other = makeSession({ sessionId: 'sess-y', identifier: 'ENG-2' });
    const live = makeSession({ status: 'live', sessionId: 'sess-x', finishedAt: undefined });

    const out = dedupSessionsPreferLive([done, other, live]);

    expect(out).toHaveLength(2);
    expect(out[0].sessionId).toBe('sess-x');
    expect(out[0].status).toBe('live');
    expect(out[1].sessionId).toBe('sess-y');
  });

  it('keeps the first occurrence when duplicates are both non-live', () => {
    const a = makeSession({ status: 'succeeded', sessionId: 'sess-x' });
    const b = makeSession({ status: 'failed', sessionId: 'sess-x' });

    const out = dedupSessionsPreferLive([a, b]);

    expect(out).toHaveLength(1);
    expect(out[0].status).toBe('succeeded');
  });

  it('keeps the live row when it appears first', () => {
    const live = makeSession({ status: 'live', sessionId: 'sess-x', finishedAt: undefined });
    const done = makeSession({ status: 'succeeded', sessionId: 'sess-x' });

    const out = dedupSessionsPreferLive([live, done]);

    expect(out).toHaveLength(1);
    expect(out[0].status).toBe('live');
  });

  it('passes sessions without a sessionId through untouched', () => {
    const a = makeSession({ sessionId: undefined });
    const b = makeSession({ sessionId: undefined });

    const out = dedupSessionsPreferLive([a, b]);

    expect(out).toHaveLength(2);
  });
});
