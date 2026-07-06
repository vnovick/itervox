import { describe, it, expect } from 'vitest';
import { buildOperatorQueueItems } from './operatorQueue';
import { makeSnapshot } from '../test/fixtures/snapshots';
import { makeIssue } from '../test/fixtures/issues';
import type { StateSnapshot, TrackerIssue } from '../types/schemas';

describe('buildOperatorQueueItems', () => {
  // RED: empty snapshot → empty groups → total 0.
  it('returns empty groups when snapshot has nothing', () => {
    const result = buildOperatorQueueItems(makeSnapshot(), []);
    expect(result.total).toBe(0);
    expect(result.groups).toEqual([]);
  });

  // Each group derives independently. We verify the five groups in turn.
  it('derives "Needs input" from snapshot.inputRequired (both sub-states)', () => {
    const snap = makeSnapshot({
      inputRequired: [
        {
          identifier: 'ENG-1',
          sessionId: 's1',
          state: 'input_required',
          context: 'PM has a clarifying question',
          queuedAt: new Date().toISOString(),
        },
        {
          identifier: 'ENG-2',
          sessionId: 's2',
          state: 'pending_input_resume',
          context: 'human reply pending',
          queuedAt: new Date().toISOString(),
        },
      ],
    });
    const result = buildOperatorQueueItems(snap, [
      makeIssue({ identifier: 'ENG-1' }),
      makeIssue({ identifier: 'ENG-2' }),
    ]);
    const needsInput = result.groups.find((g) => g.group === 'needs_input');
    expect(needsInput?.items).toHaveLength(2);
    expect(needsInput?.items.map((i) => i.identifier).sort()).toEqual(['ENG-1', 'ENG-2']);
  });

  it('derives "Retrying" from snapshot.retrying', () => {
    const snap = makeSnapshot({
      retrying: [
        {
          identifier: 'ENG-7',
          attempt: 3,
          dueAt: new Date(Date.now() + 60_000).toISOString(),
          error: 'rate_limit_exceeded',
        },
      ],
    });
    const result = buildOperatorQueueItems(snap, [makeIssue({ identifier: 'ENG-7' })]);
    const retrying = result.groups.find((g) => g.group === 'retrying');
    expect(retrying?.items).toHaveLength(1);
    expect(retrying?.items[0]?.identifier).toBe('ENG-7');
  });

  it('derives "Paused" from snapshot.paused with PausedWithPR url meta', () => {
    const snap = makeSnapshot({
      paused: ['ENG-9'],
      pausedWithPR: { 'ENG-9': 'https://github.com/x/y/pull/42' },
    });
    const result = buildOperatorQueueItems(snap, [makeIssue({ identifier: 'ENG-9' })]);
    const paused = result.groups.find((g) => g.group === 'paused');
    expect(paused?.items).toHaveLength(1);
    expect(paused?.items[0]?.meta).toContain('pull/42');
  });

  it('derives a single "Config issues" row when configInvalid is set', () => {
    const snap = makeSnapshot({
      configInvalid: {
        path: '/tmp/WORKFLOW.md',
        error: 'invalid cron expression: expected 5 fields',
        retryAttempt: 1,
      },
    });
    const result = buildOperatorQueueItems(snap, []);
    const cfg = result.groups.find((g) => g.group === 'config');
    expect(cfg?.items).toHaveLength(1);
    expect(cfg?.items[0]?.clickAction).toEqual({ type: 'navigate', path: '/settings' });
  });

  // Review group: an issue in completionState with no live reviewer running →
  // "ready for review". reviewSource = 'session' when there's a matching
  // worker history row in this session, else 'tracker'.
  it('marks reviewSource = "session" when history shows a worker run with matching appSessionId', () => {
    const sessionId = 'app-session-123';
    const snap = makeSnapshot({
      completionState: 'In Review',
      currentAppSessionId: sessionId,
      history: [
        {
          identifier: 'ENG-3',
          startedAt: new Date(Date.now() - 60_000).toISOString(),
          finishedAt: new Date().toISOString(),
          elapsedMs: 1000,
          turnCount: 5,
          tokens: 100,
          inputTokens: 80,
          outputTokens: 20,
          status: 'succeeded',
          kind: 'worker',
          appSessionId: sessionId,
        },
      ],
      running: [],
    });
    const result = buildOperatorQueueItems(snap, [
      makeIssue({ identifier: 'ENG-3', state: 'In Review' }),
    ]);
    const review = result.groups.find((g) => g.group === 'review');
    expect(review?.items).toHaveLength(1);
    expect(review?.items[0]?.reviewSource).toBe('session');
  });

  it('marks reviewSource = "tracker" when no matching session history', () => {
    const snap = makeSnapshot({
      completionState: 'In Review',
      currentAppSessionId: 'app-session-current',
      history: [
        {
          identifier: 'ENG-3',
          startedAt: new Date(Date.now() - 60_000).toISOString(),
          finishedAt: new Date().toISOString(),
          elapsedMs: 1000,
          turnCount: 0,
          tokens: 0,
          inputTokens: 0,
          outputTokens: 0,
          status: 'succeeded',
          kind: 'worker',
          appSessionId: 'old-session',
        },
      ],
    });
    const result = buildOperatorQueueItems(snap, [
      makeIssue({ identifier: 'ENG-3', state: 'In Review' }),
    ]);
    const review = result.groups.find((g) => g.group === 'review');
    expect(review?.items[0]?.reviewSource).toBe('tracker');
  });

  // Live reviewer running for the same identifier → suppress the review
  // entry (mirrors ReviewQueueSection's behaviour).
  it('suppresses review entries when a live reviewer is already running', () => {
    const snap = makeSnapshot({
      completionState: 'In Review',
      running: [
        {
          identifier: 'ENG-3',
          state: 'running',
          startedAt: new Date().toISOString(),
          turnCount: 1,
          tokens: 0,
          inputTokens: 0,
          outputTokens: 0,
          elapsedMs: 100,
          kind: 'reviewer',
        },
      ] as StateSnapshot['running'],
    });
    const result = buildOperatorQueueItems(snap, [
      makeIssue({ identifier: 'ENG-3', state: 'In Review' }),
    ]);
    const review = result.groups.find((g) => g.group === 'review');
    expect(review).toBeUndefined();
  });

  // Stable IDs — calling twice on the same input must yield the same id strings
  // so React keys don't churn between renders.
  it('produces stable IDs across repeated calls with the same input', () => {
    const snap = makeSnapshot({
      inputRequired: [
        {
          identifier: 'ENG-1',
          sessionId: 's1',
          state: 'input_required',
          context: 'q',
          queuedAt: new Date().toISOString(),
        },
      ],
    });
    const issues: TrackerIssue[] = [makeIssue({ identifier: 'ENG-1' })];
    const a = buildOperatorQueueItems(snap, issues);
    const b = buildOperatorQueueItems(snap, issues);
    expect(a.groups.flatMap((g) => g.items.map((i) => i.id))).toEqual(
      b.groups.flatMap((g) => g.items.map((i) => i.id)),
    );
  });
});
