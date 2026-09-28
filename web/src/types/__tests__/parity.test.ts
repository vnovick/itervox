import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  AutomationQueueRowSchema,
  AutomationQueueBackpressureSchema,
  AutoSwitchRowSchema,
  BackendHealthRowSchema,
  DependencyAttentionRowSchema,
  DependencyAuditRowSchema,
  DependencyCycleRowSchema,
  DependencyGraphNodeSchema,
  DependencyGraphEdgeSchema,
  FailureRowSchema,
  IssueStatusChangeSchema,
  OutboxEntryRowSchema,
  StateSnapshotSchema,
  TrackerErrorRowSchema,
  TrackerIssueSchema,
  TotalsSchema,
} from '../schemas';

// v0.2.0 audit P1-14 — every Go DTO marshaled into the dashboard SSE stream
// must round-trip through its matching Zod schema. Fixtures are emitted by
// `internal/server/dto_fixtures_test.go` (TestGenerateDTOFixturesForZodParity).
// If a Go field is added or renamed without the matching Zod update, the
// fixture lands with a new shape and this suite fails at parse-time — the
// build catches the drift before runtime SSE parses break the dashboard.

const __dirname = dirname(fileURLToPath(import.meta.url));
const fixturesDir = resolve(__dirname, '..', 'fixtures');

interface ZodIssueLike {
  path: (string | number)[];
  message: string;
}

interface SafeParseResultLike {
  success: boolean;
  error?: { issues: ZodIssueLike[] };
}

interface SchemaLike {
  safeParse: (data: unknown) => SafeParseResultLike;
}

const schemas: Record<string, SchemaLike> = {
  'AutomationQueueRow.json': AutomationQueueRowSchema,
  'AutomationQueueBackpressureRow.json': AutomationQueueBackpressureSchema,
  'AutoSwitchRow.json': AutoSwitchRowSchema,
  'BackendHealthRow.json': BackendHealthRowSchema,
  'BackendHealthRowHealthy.json': BackendHealthRowSchema,
  'DependencyAttentionRow.json': DependencyAttentionRowSchema,
  'DependencyAuditRow.json': DependencyAuditRowSchema,
  'DependencyCycleRow.json': DependencyCycleRowSchema,
  'DependencyGraphNodeRow.json': DependencyGraphNodeSchema,
  'DependencyGraphEdgeRow.json': DependencyGraphEdgeSchema,
  'FailureRow.json': FailureRowSchema,
  'IssueStatusChangeRow.json': IssueStatusChangeSchema,
  'OutboxEntryRow.json': OutboxEntryRowSchema,
  'TrackerErrorRow.json': TrackerErrorRowSchema,
  // CORE-091 / CORE-175 (Go-emitted, M6-G4)
  'Totals.json': TotalsSchema,
  'TotalsCostUnknown.json': TotalsSchema,
  'FailureAckRow.json': StateSnapshotSchema.shape.failureAcks.unwrap().element,
};

describe('Go DTO ↔ Zod schema parity (v0.2.0 audit P1-14)', () => {
  it('every fixture file must have a matching schema entry', () => {
    const names: string[] = readdirSync(fixturesDir);
    const onDisk = names.filter((name) => name.endsWith('.json')).sort();
    const mapped = Object.keys(schemas).sort();
    expect(onDisk).toEqual(mapped);
  });

  for (const [filename, schema] of Object.entries(schemas)) {
    it(`${filename} parses against its Zod schema`, () => {
      const raw: string = readFileSync(join(fixturesDir, filename), 'utf-8');
      const parsed = JSON.parse(raw) as unknown;
      const result = schema.safeParse(parsed);
      if (!result.success) {
        // Format errors as a single legible message so the test failure
        // points operators at the specific field drift.
        const issues = (result.error?.issues ?? [])
          .map((iss) => `  • ${iss.path.join('.')}: ${iss.message}`)
          .join('\n');
        throw new Error(`Zod parse failed for ${filename}:\n${issues}`);
      }
    });
  }
});

// gaps_11 G-11 — the self-reentry drop counter rides the top-level
// StateSnapshot DTO (not a fixture-backed Row), so its Go↔Zod parity is
// asserted directly here: present parses to the number, absent (omitempty /
// older daemon) parses to undefined.
describe('StateSnapshot automationDropsSelfReentryTotal parity (gaps_11 G-11)', () => {
  const minimalSnapshot = {
    generatedAt: '2026-05-25T12:00:00Z',
    counts: { running: 0, retrying: 0, paused: 0 },
    running: [],
    retrying: [],
    paused: [],
    maxConcurrentAgents: 3,
    maxRetries: 5,
    maxSwitchesPerIssuePerWindow: 2,
    switchWindowHours: 6,
    rateLimits: null,
  };

  it('parses the counter when the daemon emits it', () => {
    const parsed = StateSnapshotSchema.parse({
      ...minimalSnapshot,
      automationDropsSelfReentryTotal: 4,
    });
    expect(parsed.automationDropsSelfReentryTotal).toBe(4);
  });

  it('parses snapshots that omit the counter (omitempty / older daemon)', () => {
    const parsed = StateSnapshotSchema.parse(minimalSnapshot);
    expect(parsed.automationDropsSelfReentryTotal).toBeUndefined();
  });
});

// critical-path-ordering Task 6 — dependencyCycles/dependencyAttention ride
// the top-level StateSnapshot DTO like automationDropsSelfReentryTotal above:
// both omitempty on the wire, absent from snapshots emitted by daemons
// predating this feature. Assert both directions so a future field rename
// fails loudly here instead of silently dropping the LiveOps tile / cycle
// highlight / attention badge.
describe('StateSnapshot dependencyCycles/dependencyAttention parity (critical-path-ordering Task 6)', () => {
  const minimalSnapshot = {
    generatedAt: '2026-05-25T12:00:00Z',
    counts: { running: 0, retrying: 0, paused: 0 },
    running: [],
    retrying: [],
    paused: [],
    maxConcurrentAgents: 3,
    maxRetries: 5,
    maxSwitchesPerIssuePerWindow: 2,
    switchWindowHours: 6,
    rateLimits: null,
  };

  it('parses snapshots that include cycles and attention entries', () => {
    const parsed = StateSnapshotSchema.parse({
      ...minimalSnapshot,
      dependencyCycles: [
        { members: ['ENG-1', 'ENG-2'], kind: 'tracker', detectedAt: '2026-05-25T12:00:00Z' },
      ],
      dependencyAttention: [
        {
          identifier: 'ENG-1',
          blockers: ['ENG-2'],
          blockedSince: '2026-05-25T09:00:00Z',
          kind: 'cycle',
        },
      ],
    });
    expect(parsed.dependencyCycles).toHaveLength(1);
    expect(parsed.dependencyCycles?.[0].kind).toBe('tracker');
    expect(parsed.dependencyAttention).toHaveLength(1);
    expect(parsed.dependencyAttention?.[0].kind).toBe('cycle');
  });

  it('parses snapshots that omit both fields (omitempty / older daemon)', () => {
    const parsed = StateSnapshotSchema.parse(minimalSnapshot);
    expect(parsed.dependencyCycles).toBeUndefined();
    expect(parsed.dependencyAttention).toBeUndefined();
  });
});

// CORE-044 — lastTrackerError rides the top-level StateSnapshot DTO. Additive
// and optional: present parses to the row (dates kept as strings), absent
// (no tracker error recorded, or an older daemon) parses to undefined.
describe('StateSnapshot lastTrackerError parity (CORE-044)', () => {
  const minimalSnapshot = {
    generatedAt: '2026-09-26T10:00:00Z',
    counts: { running: 0, retrying: 0, paused: 0 },
    running: [],
    retrying: [],
    paused: [],
    maxConcurrentAgents: 3,
    maxRetries: 5,
    maxSwitchesPerIssuePerWindow: 2,
    switchWindowHours: 6,
    rateLimits: null,
  };

  it('parses a rate-limited poll error with its reset', () => {
    const parsed = StateSnapshotSchema.parse({
      ...minimalSnapshot,
      lastTrackerError: {
        at: '2026-09-26T10:00:00Z',
        op: 'poll',
        kind: 'rate_limited',
        message: 'tracker: linear rate limited until 2026-09-26T10:10:00Z',
        resetAt: '2026-09-26T10:10:00Z',
      },
    });
    expect(parsed.lastTrackerError?.kind).toBe('rate_limited');
    expect(parsed.lastTrackerError?.resetAt).toBe('2026-09-26T10:10:00Z');
    expect(parsed.lastTrackerError?.consecutiveFailures).toBeUndefined();
  });

  it('parses an outage with a failure count', () => {
    const parsed = StateSnapshotSchema.parse({
      ...minimalSnapshot,
      lastTrackerError: {
        at: '2026-09-26T10:00:00Z',
        op: 'update_state',
        kind: 'outage',
        message: 'linear: 500',
        consecutiveFailures: 3,
      },
    });
    expect(parsed.lastTrackerError?.op).toBe('update_state');
    expect(parsed.lastTrackerError?.consecutiveFailures).toBe(3);
  });

  it('parses snapshots that omit it (none recorded / older daemon)', () => {
    const parsed = StateSnapshotSchema.parse(minimalSnapshot);
    expect(parsed.lastTrackerError).toBeUndefined();
  });

  it('OutboxEntryRowSchema carries lastFailedAt when present', () => {
    const parsed = OutboxEntryRowSchema.parse({
      id: 'e',
      kind: 'create_comment',
      identifier: 'ENG-1',
      attempts: 5,
      degraded: true,
      enqueuedAt: '2026-09-26T09:00:00Z',
      nextAttemptAt: '2026-09-26T10:05:00Z',
      lastFailedAt: '2026-09-26T10:00:00Z',
    });
    expect(parsed.lastFailedAt).toBe('2026-09-26T10:00:00Z');
  });
});

// CORE-055 — backendHealth / autoSwitches ride the snapshot as optional,
// additive fields; issues carry an optional autoSwitch.
describe('StateSnapshot backend health parity (CORE-055)', () => {
  const minimalSnapshot = {
    generatedAt: '2026-05-25T12:00:00Z',
    counts: { running: 0, retrying: 0, paused: 0 },
    running: [],
    retrying: [],
    paused: [],
    maxConcurrentAgents: 3,
    maxRetries: 5,
    maxSwitchesPerIssuePerWindow: 2,
    switchWindowHours: 6,
    rateLimits: null,
  };

  it('parses a pre-field snapshot (older daemon)', () => {
    const parsed = StateSnapshotSchema.parse(minimalSnapshot);
    expect(parsed.backendHealth).toBeUndefined();
    expect(parsed.autoSwitches).toBeUndefined();
  });

  it('parses a populated snapshot, keeping a null limitedUntil as null', () => {
    const parsed = StateSnapshotSchema.parse({
      ...minimalSnapshot,
      backendHealth: [
        {
          backend: 'claude',
          status: 'limited',
          limitedUntil: '2026-05-25T15:00:00Z',
          heldIssues: 1,
          reroutedIssues: 2,
        },
        {
          backend: 'codex',
          status: 'healthy',
          limitedUntil: null,
          heldIssues: 0,
          reroutedIssues: 0,
        },
        {
          backend: 'codex',
          status: 'brand-new-status',
          limitedUntil: null,
          heldIssues: 0,
          reroutedIssues: 0,
        },
      ],
      autoSwitches: [
        {
          identifier: 'ENG-1',
          source: 'backend_fallback',
          fromBackend: 'claude',
          toBackend: 'codex',
        },
      ],
    });
    expect(parsed.backendHealth?.[0].limitedUntil).toBe('2026-05-25T15:00:00Z');
    expect(parsed.backendHealth?.[1].limitedUntil).toBeNull();
    expect(parsed.backendHealth?.[2].status).toBe('warning'); // unknown degrades visibly, never to healthy
    expect(parsed.autoSwitches?.[0].source).toBe('backend_fallback');
  });

  it('TrackerIssue carries an optional autoSwitch', () => {
    const issue = { identifier: 'ENG-1', title: 't', state: 'Todo', orchestratorState: 'idle' };
    expect(TrackerIssueSchema.parse(issue).autoSwitch).toBeUndefined();
    const parsed = TrackerIssueSchema.parse({
      ...issue,
      autoSwitch: { identifier: 'ENG-1', source: 'automation', toBackend: 'codex' },
    });
    expect(parsed.autoSwitch?.toBackend).toBe('codex');
  });
});
