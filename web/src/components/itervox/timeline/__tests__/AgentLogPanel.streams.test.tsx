// CORE-099 — Timeline already streams the selected issue's log and hands the
// panel its slice; the panel must not open a second stream for the same issue
// (measured: 2 log-streams per Timeline tab before this fix).
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from '@testing-library/react';
import { AgentLogPanel } from '../AgentLogPanel';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { makeRunningRow, makeSnapshot } from '../../../../test/fixtures/snapshots';
import type { IssueLogEntry } from '../../../../types/schemas';

const calls = vi.hoisted(() => [] as [string, boolean][]);
vi.mock('../../../../queries/logs', () => ({
  useIssueLogs: (identifier: string, live: boolean) => {
    calls.push([identifier, live]);
    return { data: [] as IssueLogEntry[] };
  },
}));

beforeEach(() => {
  calls.length = 0;
  Element.prototype.scrollIntoView = vi.fn();
  useItervoxStore.setState({
    snapshot: makeSnapshot({ running: [makeRunningRow({ identifier: 'ENG-1' })] }),
  });
});

describe('AgentLogPanel log subscription (CORE-099)', () => {
  it('opens no stream or fetch of its own when the caller passes the entries', () => {
    render(<AgentLogPanel identifier="ENG-1" logSlice={[]} />);
    // An empty identifier disables both the live stream and the one-shot query.
    expect(calls.every(([id]) => id === '')).toBe(true);
  });

  it('streams the live issue itself when no slice is given', () => {
    render(<AgentLogPanel identifier="ENG-1" />);
    expect(calls).toContainEqual(['ENG-1', true]);
  });
});
