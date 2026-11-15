import { describe, expect, it } from 'vitest';
import { pickFocusIssue } from '../focusPick';
import {
  makeHistoryRow,
  makeInputRequiredRow,
  makePendingInputResumeRow,
  makeRunningRow,
} from '../../test/fixtures/snapshots';

const at = (iso: string) => ({ startedAt: iso });

describe('pickFocusIssue (the issue view without an identifier)', () => {
  it('is null without a snapshot or anything to show (the empty state)', () => {
    expect(pickFocusIssue(null)).toBeNull();
    expect(pickFocusIssue({ running: [], history: [], inputRequired: [] })).toBeNull();
  });

  it('prefers an issue waiting for input — the one waiting longest', () => {
    const pick = pickFocusIssue({
      running: [makeRunningRow({ identifier: 'RUN-1' })],
      history: [makeHistoryRow({ identifier: 'DONE-1' })],
      inputRequired: [
        makeInputRequiredRow({ identifier: 'ASK-NEW', queuedAt: '2026-10-01T12:05:00Z' }),
        makeInputRequiredRow({ identifier: 'ASK-OLD', queuedAt: '2026-10-01T12:00:00Z' }),
      ],
    });
    expect(pick).toEqual({ identifier: 'ASK-OLD', reason: 'input_required' });
  });

  it('skips a reply already received and takes the most recently started run', () => {
    const pick = pickFocusIssue({
      running: [
        makeRunningRow({ identifier: 'RUN-OLD', ...at('2026-10-01T10:00:00Z') }),
        makeRunningRow({ identifier: 'RUN-NEW', ...at('2026-10-01T11:00:00Z') }),
        makeRunningRow({ identifier: 'RUN-MID', ...at('2026-10-01T10:30:00Z') }),
      ],
      history: [makeHistoryRow({ identifier: 'DONE-1' })],
      inputRequired: [makePendingInputResumeRow({ identifier: 'RESUMING-1' })],
    });
    expect(pick).toEqual({ identifier: 'RUN-NEW', reason: 'running' });
  });

  it('falls back to the most recently finished run', () => {
    const pick = pickFocusIssue({
      running: [],
      history: [
        makeHistoryRow({ identifier: 'DONE-OLD', finishedAt: '2026-10-01T09:00:00Z' }),
        makeHistoryRow({ identifier: 'DONE-NEW', finishedAt: '2026-10-01T09:30:00Z' }),
      ],
    });
    expect(pick).toEqual({ identifier: 'DONE-NEW', reason: 'finished' });
  });
});
