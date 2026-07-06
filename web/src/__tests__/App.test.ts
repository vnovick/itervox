import { describe, expect, it } from 'vitest';
import { buildSnapshotInvalidationFingerprint } from '../App';

describe('buildSnapshotInvalidationFingerprint', () => {
  it('changes when an input-required row becomes a pending resume for the same identifier', () => {
    const waiting = buildSnapshotInvalidationFingerprint({
      running: [],
      retrying: [],
      paused: [],
      pausedWithPR: {},
      inputRequired: [
        {
          identifier: 'ENG-1',
          sessionId: 'session-1',
          state: 'input_required',
          context: 'Need approval',
          queuedAt: '2026-04-15T00:00:00Z',
        },
      ],
    } as never);
    const pending = buildSnapshotInvalidationFingerprint({
      running: [],
      retrying: [],
      paused: [],
      pausedWithPR: {},
      inputRequired: [
        {
          identifier: 'ENG-1',
          sessionId: 'session-1',
          state: 'pending_input_resume',
          context: 'Reply received, waiting to resume.',
          queuedAt: '2026-04-15T00:00:00Z',
        },
      ],
    } as never);

    expect(waiting).not.toBeNull();
    expect(pending).not.toBeNull();
    expect(waiting).not.toBe(pending);
  });
});
