import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  AutomationQueueBackpressureSchema,
  DependencyAuditRowSchema,
  AutomationQueueRowSchema,
  AllowedAgentActionSchema,
} from '../schemas';
import { AUTOMATION_TRIGGER_TYPES } from '../automationTriggers';

// Regression guard for the "Board/Deps go Offline" class of bug: the daemon
// emits supportedAgentActions/allowedActions from internal/config/agent_actions.go.
// If the Go side adds an action the Zod enum doesn't list, StateSnapshotSchema.parse
// rejects the ENTIRE snapshot and the dashboard silently nulls out (every
// snapshot-derived panel shows "Offline"/"Loading"). `merge_pr` shipped on the
// Go side without this enum being updated, which is exactly what happened.
describe('agent action parity with internal/config/agent_actions.go', () => {
  it('Zod AllowedAgentActionSchema covers every Go AgentAction constant', () => {
    // vitest runs with cwd = the web/ package root.
    const goPath = resolve(process.cwd(), '../internal/config/agent_actions.go');
    const src = readFileSync(goPath, 'utf8');
    const goActions = [...src.matchAll(/AgentAction\w+\s*=\s*"([a-z_]+)"/g)].map((m) =>
      String(m[1]),
    );
    expect(goActions.length).toBeGreaterThanOrEqual(6);
    const zodValues = AllowedAgentActionSchema.options as readonly string[];
    for (const action of goActions) {
      expect(
        zodValues,
        `Zod AllowedAgentActionSchema is missing "${action}" — add it or the whole snapshot fails to parse`,
      ).toContain(action);
    }
  });
});

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

// FE-2 regression guard: automation trigger types. Go accepts pr_merged etc.;
// a missing TS enum value makes StateSnapshotSchema.parse throw on the whole
// snapshot — the FE-1 failure class.
describe('automation trigger parity with internal/config/automations.go', () => {
  it('AUTOMATION_TRIGGER_TYPES covers every Go AutomationTrigger constant', () => {
    // vitest runs with cwd = the web/ package root.
    const goPath = resolve(process.cwd(), '../internal/config/automations.go');
    const src = readFileSync(goPath, 'utf8');
    // Real declaration shape is untyped, e.g. `AutomationTriggerPRMerged = "pr_merged"`
    // inside a single const ( ... ) block — no explicit `AutomationTriggerType` annotation.
    const goTriggers = [...src.matchAll(/AutomationTrigger\w+\s*=\s*"([a-z_]+)"/g)].map((m) =>
      String(m[1]),
    );
    expect(goTriggers.length).toBeGreaterThanOrEqual(10);
    for (const trigger of goTriggers) {
      expect(
        AUTOMATION_TRIGGER_TYPES as readonly string[],
        `TS AUTOMATION_TRIGGER_TYPES is missing "${trigger}" — the whole snapshot fails to parse`,
      ).toContain(trigger);
    }
  });
});
