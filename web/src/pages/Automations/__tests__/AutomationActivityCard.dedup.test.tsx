import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { AutomationActivityCard } from '../AutomationActivityCard';
import { makeAutomation, makeHistoryRow, makeRunningRow } from '../../../test/fixtures/snapshots';
import { formatRFC3339, minutesAgo } from '../../../test/fixtures/time';
import type { HistoryRow, RunningRow } from '../../../types/schemas';

// gaps_11 G-13(a) — this test previously asserted against a LOCAL mirror of
// the key-building logic and never rendered the component, so its
// console.error spy could never fire. It now renders the REAL
// AutomationActivityCard with fixtures engineered to collide under the old
// buggy key scheme (`sessionId ?? identifier-timestamp` WITHOUT the
// live/done suffix). If someone reverts the `-live`/`-done` suffix in
// AutomationActivityCard.tsx, React logs an "Encountered two children with
// the same key" error and the assertions below fail.

const renderCard = (props: Parameters<typeof AutomationActivityCard>[0]) =>
  render(
    <MemoryRouter>
      <AutomationActivityCard {...props} />
    </MemoryRouter>,
  );

function withoutSessionId<T extends RunningRow | HistoryRow>(row: T): T {
  return { ...row, sessionId: undefined };
}

function duplicateKeyWarnings(spy: MockInstance): unknown[][] {
  return spy.mock.calls.filter((args) =>
    args.some((a) => typeof a === 'string' && a.toLowerCase().includes('same key')),
  );
}

describe('AutomationActivityCard dedup (gaps_11 G-13, codex-B6)', () => {
  let consoleError: MockInstance;

  beforeEach(() => {
    consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
  });

  afterEach(() => {
    consoleError.mockRestore();
  });

  it('renders distinct keys for live+done rows that share identifier+timestamp without a sessionId', () => {
    // Engineered collision under the OLD key scheme: neither row has a
    // sessionId, both fall back to `identifier-timestamp`, and the live
    // row's startedAt equals the history row's finishedAt. Only the
    // live/done suffix keeps the keys distinct.
    const sharedTimestamp = formatRFC3339(minutesAgo(5));
    const automation = makeAutomation({ id: 'suffix-guard' });
    const live = withoutSessionId(
      makeRunningRow({
        identifier: 'ENG-1',
        automationId: 'suffix-guard',
        startedAt: sharedTimestamp,
      }),
    );
    const done = withoutSessionId(
      makeHistoryRow({
        identifier: 'ENG-1',
        automationId: 'suffix-guard',
        finishedAt: sharedTimestamp,
        status: 'succeeded',
      }),
    );

    renderCard({ automation, running: [live], history: [done] });

    const rows = screen.getByTestId('automation-runs-suffix-guard').querySelectorAll('li');
    // Without a sessionId there is no safe dedup identity — both rows render.
    expect(rows.length).toBe(2);
    expect(duplicateKeyWarnings(consoleError)).toEqual([]);
  });

  it('dedups a run that appears as both live and done with the same sessionId, preferring the live row', () => {
    const automation = makeAutomation({ id: 'dedup-shared' });
    const live = makeRunningRow({
      identifier: 'ENG-2',
      automationId: 'dedup-shared',
      sessionId: 'sess-shared',
      startedAt: formatRFC3339(minutesAgo(3)),
    });
    const done = makeHistoryRow({
      identifier: 'ENG-2',
      automationId: 'dedup-shared',
      sessionId: 'sess-shared',
      finishedAt: formatRFC3339(minutesAgo(2)),
      status: 'succeeded',
    });

    renderCard({ automation, running: [live], history: [done] });

    const rows = screen.getByTestId('automation-runs-dedup-shared').querySelectorAll('li');
    expect(rows.length).toBe(1);
    // Live row wins during the stale-running window.
    expect(rows[0].textContent).toContain('running');
    expect(rows[0].textContent).not.toContain('succeeded');
    expect(duplicateKeyWarnings(consoleError)).toEqual([]);
  });

  it('dedups duplicate sessionIds within history alone', () => {
    const automation = makeAutomation({ id: 'dedup-history' });
    const history = [
      makeHistoryRow({
        identifier: 'ENG-3',
        automationId: 'dedup-history',
        sessionId: 'sess-dup',
        finishedAt: formatRFC3339(minutesAgo(10)),
        status: 'succeeded',
      }),
      makeHistoryRow({
        identifier: 'ENG-3',
        automationId: 'dedup-history',
        sessionId: 'sess-dup',
        finishedAt: formatRFC3339(minutesAgo(10)),
        status: 'succeeded',
      }),
    ];

    renderCard({ automation, running: [], history });

    const rows = screen.getByTestId('automation-runs-dedup-history').querySelectorAll('li');
    expect(rows.length).toBe(1);
    expect(duplicateKeyWarnings(consoleError)).toEqual([]);
  });
});
