// #211 — the board must not re-render for snapshot fields it does not read.
// The Dashboard page selects many snapshot branches (automation queue,
// failures, outbox…); before, any change to one of them — and, through the
// whole-snapshot Notifications count, every SSE frame — re-rendered the board.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, screen } from '@testing-library/react';
import Dashboard from '../index';
import { render } from '../../../test/render';
import { authedFetch } from '../../../auth/authedFetch';
import { makeRunningRow, makeSnapshot } from '../../../test/fixtures/snapshots';
import { makeIssues } from '../../../test/fixtures/issues';
import { useItervoxStore } from '../../../store/itervoxStore';
import type { StateSnapshot } from '../../../types/schemas';

const columnRenders = vi.hoisted(() => ({ count: 0 }));

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
vi.mock('../../../components/itervox/NarrativeFeed', () => ({ NarrativeFeed: () => null }));
vi.mock('../../../components/itervox/BoardColumn', () => ({
  default: ({ state, issues }: { state: string; issues: { identifier: string }[] }) => {
    columnRenders.count += 1;
    return (
      <div data-testid={`board-column-${state}`}>
        {issues.map((i) => (
          <span key={i.identifier}>{i.identifier}</span>
        ))}
      </div>
    );
  },
}));

const mockFetch = vi.mocked(authedFetch);
const issues = makeIssues(3, () => ({ state: 'In Progress' }));

function json(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

const base = makeSnapshot({
  trackerKind: 'linear',
  activeStates: ['In Progress'],
  history: [],
});

function push(next: StateSnapshot) {
  act(() => {
    useItervoxStore.getState().setSnapshot(next);
  });
}

async function mountBoard() {
  render(<Dashboard />, { snapshot: base });
  // Wait for the issue list to land on the board.
  await screen.findByText('DEMO-3');
}

beforeEach(() => {
  columnRenders.count = 0;
  mockFetch.mockReset();
  mockFetch.mockImplementation((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    return Promise.resolve(json(url.endsWith('/api/v1/issues') ? issues : []));
  });
});

describe('Dashboard board renders', () => {
  it('board does not re-render on an unrelated snapshot field', async () => {
    await mountBoard();
    const before = columnRenders.count;
    expect(before).toBeGreaterThan(0);

    // generatedAt-only frame.
    push({ ...structuredClone(base), generatedAt: '2026-02-01T00:00:01Z' });
    // Fields the Dashboard page selects but the board does not read.
    push({
      ...structuredClone(base),
      generatedAt: '2026-02-01T00:00:02Z',
      recentFailures: [
        {
          kind: 'tracker',
          message: 'poll failed',
          occurredAt: '2026-02-01T00:00:02Z',
          recordedAt: '2026-02-01T00:00:02Z',
          count: 1,
        },
      ],
    });
    push({
      ...structuredClone(base),
      generatedAt: '2026-02-01T00:00:03Z',
      maxConcurrentAgents: 9,
      sshHosts: [{ host: 'build-1', description: 'builder' }],
    });

    expect(useItervoxStore.getState().snapshot?.maxConcurrentAgents).toBe(9);
    expect(columnRenders.count).toBe(before);
  });

  it('board re-renders when a field it reads changes (control)', async () => {
    await mountBoard();
    const before = columnRenders.count;
    push({
      ...structuredClone(base),
      generatedAt: '2026-02-01T00:00:04Z',
      running: [makeRunningRow({ identifier: 'DEMO-1', backend: 'codex' })],
    });
    expect(columnRenders.count).toBeGreaterThan(before);
  });
});
