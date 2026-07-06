import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  AutomationQueueRowSchema,
  AutomationQueueBackpressureSchema,
  DependencyAuditRowSchema,
  DependencyGraphNodeSchema,
  DependencyGraphEdgeSchema,
  IssueStatusChangeSchema,
  StateSnapshotSchema,
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
  'DependencyAuditRow.json': DependencyAuditRowSchema,
  'DependencyGraphNodeRow.json': DependencyGraphNodeSchema,
  'DependencyGraphEdgeRow.json': DependencyGraphEdgeSchema,
  'IssueStatusChangeRow.json': IssueStatusChangeSchema,
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
