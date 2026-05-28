import { describe, expect, it } from 'vitest';

import {
  AutomationQueueBackpressureSchema,
  DependencyAuditRowSchema,
  AutomationQueueRowSchema,
} from '../schemas';

// v0.2.0 audit P1-5 — Zod side. Even though the Go DTOs now use *time.Time
// for optional time fields, the Zod refine drops "0001-01-01T00:00:00Z" as
// undefined defensively, so any future Go DTO that forgets the pointer
// conversion does not break the dashboard at render time.
describe('optionalTimeString refine (v0.2.0 audit P1-5)', () => {
  it('drops year-0001 sentinel as undefined for AutomationQueueBackpressureSchema', () => {
    const result = AutomationQueueBackpressureSchema.parse({
      length: 0,
      maxLength: 100,
      saturated: false,
      pausedProducers: false,
      rejectedSinceBoot: 0,
      lastRejectedAt: '0001-01-01T00:00:00Z',
    });
    expect(result.lastRejectedAt).toBeUndefined();
  });

  it('drops year-0001 sentinel across DependencyAuditRow optional time fields', () => {
    const result = DependencyAuditRowSchema.parse({
      identifier: 'ENG-1',
      issueState: 'Done',
      status: 'unblocked',
      wasBlocked: true,
      firstBlockedAt: '0001-01-01T00:00:00Z',
      unblockedAt: '0001-01-01T00:00:00Z',
      lastAuditedAt: '0001-01-01T00:00:00Z',
    });
    expect(result.firstBlockedAt).toBeUndefined();
    expect(result.unblockedAt).toBeUndefined();
    expect(result.lastAuditedAt).toBeUndefined();
  });

  it('preserves real timestamps', () => {
    const result = AutomationQueueRowSchema.parse({
      id: 'q-1',
      automationId: 'a-1',
      triggerType: 'cron',
      identifier: 'ENG-1',
      profile: 'implementer',
      status: 'queued',
      reason: 'queue_full',
      queuedAt: '2026-05-25T12:00:00Z',
      firedAt: '2026-05-25T12:05:00Z',
      lastAttemptAt: '2026-05-25T12:04:00Z',
      attemptCount: 0,
    });
    expect(result.lastAttemptAt).toBe('2026-05-25T12:04:00Z');
  });
});

// v0.2.0 audit P1-11 — int64 → number precision loss. JavaScript numbers
// lose precision above 2^53. The DependencyAuditRow.lastTransitionVersion
// field is int64 on the Go side; the Zod schema now bounds it to the safe
// integer range so silent corruption becomes a parse error instead.
describe('optionalSafeInt guard (v0.2.0 audit P1-11)', () => {
  it('rejects values above Number.MAX_SAFE_INTEGER', () => {
    const result = DependencyAuditRowSchema.safeParse({
      identifier: 'ENG-1',
      issueState: 'Done',
      status: 'unblocked',
      wasBlocked: true,
      lastTransitionVersion: 2 ** 60,
    });
    expect(result.success).toBe(false);
  });

  it('accepts values within the safe integer range', () => {
    const result = DependencyAuditRowSchema.parse({
      identifier: 'ENG-1',
      issueState: 'Done',
      status: 'unblocked',
      wasBlocked: true,
      lastTransitionVersion: 12345,
    });
    expect(result.lastTransitionVersion).toBe(12345);
  });

  it('allows the field to be absent', () => {
    const result = DependencyAuditRowSchema.parse({
      identifier: 'ENG-1',
      issueState: 'Done',
      status: 'unblocked',
      wasBlocked: true,
    });
    expect(result.lastTransitionVersion).toBeUndefined();
  });
});
