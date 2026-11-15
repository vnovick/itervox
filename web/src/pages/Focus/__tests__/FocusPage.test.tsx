// The issue view (/focus, /focus/:identifier): which issue it shows, the
// "Now" column (summary, last tool call, counters, steer box) and the
// Changes column (the shared diff viewer), and the empty state.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import FocusPage from '..';
import { render } from '../../../test/render';
import { authedFetch } from '../../../auth/authedFetch';
import {
  makeHistoryRow,
  makeInputRequiredRow,
  makeRunningRow,
  makeSnapshot,
} from '../../../test/fixtures/snapshots';
import { makeInputRequiredIssue, makeIssue, makeRunningIssue } from '../../../test/fixtures/issues';
import type { StateSnapshot, TrackerIssue } from '../../../types/schemas';
import type * as IssueChangesTabModule from '../../../components/itervox/changes/IssueChangesTab';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));

// Records the props the Changes column passes to the shared diff viewer
// (rendering the real one), so the issue state the "Move to" checkbox needs
// can be checked.
const changesProps = vi.hoisted(() => [] as Record<string, unknown>[]);
vi.mock('../../../components/itervox/changes/IssueChangesTab', async (importOriginal) => {
  const actual = await importOriginal<typeof IssueChangesTabModule>();
  const { createElement } = await import('react');
  return {
    default: (props: Parameters<typeof actual.default>[0]) => {
      changesProps.push(props as Record<string, unknown>);
      return createElement(actual.default, props);
    },
  };
});

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const T = '2026-10-01T12:00:00Z';
function runContext(identifier: string, live: boolean) {
  return {
    identifier,
    run: {
      runId: `run-${identifier}`,
      live,
      profile: 'coder',
      backend: 'claude',
      startedAt: T,
      currentStep: 'Updating the session middleware, then running the tests.',
      lastToolCall: { name: 'Bash', target: 'go test ./internal/session/...', at: T },
      toolCalls: 14,
      turnCount: 3,
      playbook: 'refactor',
      ...(live ? {} : { outcome: 'succeeded', finishedAt: T }),
    },
    changedFiles: [],
    handoffs: [],
  };
}

function serve(issues: TrackerIssue[], opts: { live?: boolean } = {}) {
  vi.mocked(authedFetch).mockImplementation((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(url, 'http://x').pathname;
    const id = decodeURIComponent(path.split('/issues/')[1]?.split('/')[0] ?? '');
    const issue = issues.find((i) => i.identifier === id);
    if (path === '/api/v1/issues') return Promise.resolve(json(issues));
    if (path.endsWith('/context'))
      return Promise.resolve(issue ? json(runContext(id, opts.live ?? true)) : json({}, 404));
    if (path.endsWith('/diff'))
      return Promise.resolve(
        json({ identifier: id, base: 'main', mergeBase: 'abc', files: [], hunksAllowed: true }),
      );
    if (path.endsWith('/decisions')) return Promise.resolve(json({ identifier: id, runs: [] }));
    if (path === '/api/v1/steering/phrases') return Promise.resolve(json({ phrases: [] }));
    if (issue && path === `/api/v1/issues/${encodeURIComponent(id)}`)
      return Promise.resolve(json(issue));
    return Promise.resolve(json({ error: 'not found' }, 404));
  });
}

const page = (
  <Routes>
    <Route path="/focus/:identifier?" element={<FocusPage />} />
  </Routes>
);

function snap(over: Partial<StateSnapshot>): StateSnapshot {
  return { ...makeSnapshot({ running: [] }), capabilities: ['run_context'], ...over };
}

beforeEach(() => {
  vi.mocked(authedFetch).mockReset();
});
afterEach(() => {
  vi.mocked(authedFetch).mockReset();
});

describe('FocusPage', () => {
  it('shows the empty state when nothing needs input, runs or has finished', async () => {
    serve([]);
    render(page, { route: '/focus', snapshot: snap({ history: [], inputRequired: [] }) });
    const empty = await screen.findByTestId('focus-empty');
    expect(empty).toHaveTextContent('Nothing to focus on yet');
    expect(within(empty).getByRole('link', { name: 'Open the board' })).toHaveAttribute(
      'href',
      '/',
    );
  });

  it('without an identifier picks the issue waiting for input over a running one', async () => {
    serve([
      makeRunningIssue({ identifier: 'RUN-1' }),
      makeInputRequiredIssue({ identifier: 'ASK-1', title: 'Which region?' }),
    ]);
    render(page, {
      route: '/focus',
      snapshot: snap({
        running: [makeRunningRow({ identifier: 'RUN-1' })],
        inputRequired: [makeInputRequiredRow({ identifier: 'ASK-1' })],
      }),
    });
    expect(await screen.findByTestId('focus-title')).toHaveTextContent('Which region?');
    expect(screen.getByTestId('focus-page')).toHaveAttribute('data-identifier', 'ASK-1');
    expect(screen.getByText('Agent needs your input')).toBeInTheDocument();
  });

  it('shows the live run: summary, last tool call, counters, steer box and changes', async () => {
    serve([makeRunningIssue({ identifier: 'WEB-207', title: 'Migrate session store' })]);
    render(page, {
      route: '/focus/WEB-207',
      snapshot: snap({ running: [makeRunningRow({ identifier: 'WEB-207' })] }),
    });
    expect(await screen.findByTestId('focus-last-tool')).toHaveTextContent(
      'Bash · go test ./internal/session/...',
    );
    const now = screen.getByTestId('focus-now');
    expect(within(now).getByRole('heading', { name: 'Now' })).toBeInTheDocument();
    expect(now).toHaveTextContent('Updating the session middleware, then running the tests.');
    expect(now).toHaveTextContent('coder · claude');
    const counters = within(now).getByRole('list', { name: 'Run counters' });
    expect(
      within(counters)
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual(['14 tool calls', '3 turns', 'Playbook: refactor']);
    expect(await screen.findByTestId('issue-steer-box')).toBeInTheDocument();
    const changes = screen.getByTestId('focus-changes');
    expect(within(changes).getByRole('heading', { name: 'Changes' })).toBeInTheDocument();
    expect(await within(changes).findByTestId('issue-changes-tab')).toBeInTheDocument();
    // The diff viewer gets the issue's tracker state, so "Move to <active
    // state>" stays hidden for an issue that is already active.
    expect(changesProps.at(-1)).toMatchObject({ identifier: 'WEB-207', issueState: 'In Progress' });
  });

  it('falls back to the last finished run; no steer box without a live run', async () => {
    serve([makeIssue({ identifier: 'DONE-1', title: 'Shipped thing', state: 'Done' })], {
      live: false,
    });
    render(page, {
      route: '/focus',
      snapshot: snap({ history: [makeHistoryRow({ identifier: 'DONE-1' })] }),
    });
    expect(await screen.findByRole('heading', { name: 'Last run' })).toBeInTheDocument();
    expect(screen.getByTestId('focus-now')).toHaveTextContent('Outcome: succeeded');
    expect(screen.queryByTestId('issue-steer-box')).toBeNull();
  });

  it('a pinned identifier the tracker does not know shows "not found"', async () => {
    serve([]);
    render(page, { route: '/focus/GONE-1', snapshot: snap({}) });
    expect(await screen.findByText('GONE-1 was not found')).toBeInTheDocument();
  });
});
