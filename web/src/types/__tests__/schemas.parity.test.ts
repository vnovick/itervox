import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  AutomationQueueBackpressureSchema,
  DependencyAuditRowSchema,
  AutomationQueueRowSchema,
  AllowedAgentActionSchema,
  DependencyGraphEdgeSchema,
  DepsAnalyzeJobSchema,
  OutboxEntryRowSchema,
  StateSnapshotSchema,
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
// unified-dependency-graph Task 8 — the edge row grew four new optional
// fields (confidence, stale, overridden, gating) on the backend. Old
// snapshots written before this field set landed must still parse (no `kind`
// field was ever added on the wire — that draft was reverted in Task 7 as a
// duplicate of `origin`).
describe('DependencyGraphEdgeSchema (unified-dependency-graph Task 8)', () => {
  it('parses an edge row carrying all new inferred-edge fields', () => {
    const result = DependencyGraphEdgeSchema.safeParse({
      id: 'edge-1',
      sourceIdentifier: 'ENG-5',
      targetIdentifier: 'ENG-1',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
      evidence: 'title mentions depends on ENG-5',
      confidence: 0.82,
      stale: false,
      overridden: false,
      gating: true,
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.confidence).toBe(0.82);
      expect(result.data.stale).toBe(false);
      expect(result.data.overridden).toBe(false);
      expect(result.data.gating).toBe(true);
    }
  });

  it('parses an old-shaped edge row missing all four new fields', () => {
    const result = DependencyGraphEdgeSchema.safeParse({
      id: 'edge-2',
      sourceIdentifier: 'ENG-6',
      targetIdentifier: 'ENG-2',
      resolved: true,
      sourceKnown: true,
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.confidence).toBeUndefined();
      expect(result.data.stale).toBeUndefined();
      expect(result.data.overridden).toBeUndefined();
      expect(result.data.gating).toBeUndefined();
    }
  });
});

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

// analyzer-autonomy Task 5 — DepsAnalyzeJobRow.Trigger (internal/server/server.go)
// is additive JSON (`omitempty`, "manual" | "auto"). The Zod side must parse
// both an old-shaped job row that predates the field and a job row carrying
// either value, and fall back to 'manual' (the safe default — most jobs are
// operator-initiated) rather than failing the whole parse on an unrecognized
// value, matching this file's enum-catch idiom used elsewhere (e.g.
// DependencyGraphEdgeSchema's `origin`).
describe('DepsAnalyzeJobSchema trigger field (analyzer-autonomy Task 5)', () => {
  const base = {
    jobId: 'job-1',
    status: 'running' as const,
    queuedAt: '2026-05-25T12:00:00Z',
  };

  it('parses a job row with trigger omitted (older daemon predating the field)', () => {
    const result = DepsAnalyzeJobSchema.parse(base);
    expect(result.trigger).toBeUndefined();
  });

  it('parses a job row with trigger: "manual"', () => {
    const result = DepsAnalyzeJobSchema.parse({ ...base, trigger: 'manual' });
    expect(result.trigger).toBe('manual');
  });

  it('parses a job row with trigger: "auto"', () => {
    const result = DepsAnalyzeJobSchema.parse({ ...base, trigger: 'auto' });
    expect(result.trigger).toBe('auto');
  });

  it('falls back to "manual" for an unrecognized trigger value instead of failing the parse', () => {
    const result = DepsAnalyzeJobSchema.parse({ ...base, trigger: 'something-new' });
    expect(result.trigger).toBe('manual');
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

// write-ahead-outbox design, Task 4 — OutboxEntryRowSchema mirrors
// server.OutboxEntryRow; StateSnapshot.outboxEntries/outboxSyncing are
// additive (omitempty on the wire). Both directions must parse: a full row
// with every optional field set, and an older/empty snapshot missing the
// fields entirely (daemon predating the feature).
describe('OutboxEntryRowSchema and StateSnapshot outbox fields (write-ahead-outbox design, Task 4)', () => {
  it('parses a full outbox entry row', () => {
    const result = OutboxEntryRowSchema.safeParse({
      id: 'entry-1',
      kind: 'update_state',
      identifier: 'ENG-1',
      targetState: 'Done',
      attempts: 3,
      lastError: 'tracker: 500',
      degraded: false,
      enqueuedAt: '2026-05-25T12:00:00Z',
      nextAttemptAt: '2026-05-25T12:05:00Z',
    });
    expect(result.success).toBe(true);
  });

  it('parses a create_comment entry row missing targetState', () => {
    const result = OutboxEntryRowSchema.safeParse({
      id: 'entry-2',
      kind: 'create_comment',
      identifier: 'ENG-2',
      attempts: 0,
      enqueuedAt: '2026-05-25T12:00:00Z',
      nextAttemptAt: '2026-05-25T12:00:00Z',
    });
    expect(result.success).toBe(true);
  });

  it('StateSnapshotSchema parses outboxEntries + outboxSyncing when present', () => {
    const result = StateSnapshotSchema.safeParse({
      generatedAt: '2026-05-25T12:00:00Z',
      counts: { running: 0, retrying: 0, paused: 0 },
      running: [],
      retrying: [],
      paused: [],
      maxConcurrentAgents: 1,
      maxRetries: 5,
      maxSwitchesPerIssuePerWindow: 2,
      switchWindowHours: 6,
      rateLimits: null,
      outboxEntries: [
        {
          id: 'entry-1',
          kind: 'update_state',
          identifier: 'ENG-1',
          targetState: 'Done',
          attempts: 1,
          enqueuedAt: '2026-05-25T12:00:00Z',
          nextAttemptAt: '2026-05-25T12:00:00Z',
        },
      ],
      outboxSyncing: ['ENG-1'],
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.outboxEntries).toHaveLength(1);
      expect(result.data.outboxSyncing).toEqual(['ENG-1']);
    }
  });

  it('StateSnapshotSchema parses an old snapshot missing outbox fields entirely', () => {
    const result = StateSnapshotSchema.safeParse({
      generatedAt: '2026-05-25T12:00:00Z',
      counts: { running: 0, retrying: 0, paused: 0 },
      running: [],
      retrying: [],
      paused: [],
      maxConcurrentAgents: 1,
      maxRetries: 5,
      maxSwitchesPerIssuePerWindow: 2,
      switchWindowHours: 6,
      rateLimits: null,
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.outboxEntries).toBeUndefined();
      expect(result.data.outboxSyncing).toBeUndefined();
    }
  });
});
