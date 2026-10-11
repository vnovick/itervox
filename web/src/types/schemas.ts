/**
 * Zod schemas for all shapes returned by the Itervox HTTP API.
 *
 * These are the authoritative type definitions. `itervox.ts` re-exports the
 * inferred TypeScript types for backward compatibility with existing imports.
 *
 * At every API boundary (fetch + SSE parse), call `.parse()` so a field rename
 * in the Go server throws a clear error in the browser console instead of
 * producing silent undefined values.
 */
import { z } from 'zod';
import { AUTOMATION_TRIGGER_TYPES } from './automationTriggers';
import {
  extraKeys,
  type AutomationExtraKeys,
  type AutomationUnknownFields,
  type JsonValue,
  type RawAutomationPolicy,
  type RawAutomationTrigger,
} from './configRoundTrip';

/**
 * Optional timestamp schema. Belt-and-braces guard against the v0.2.0
 * audit P1-5 year-0001 leak: even after the Go side migrated time.Time-typed
 * optional fields to *time.Time, a future DTO addition could re-introduce
 * the sentinel. This refine drops "0001-01-01T00:00:00Z" as if the field
 * were undefined so callers do not have to repeat the guard at every
 * consumer.
 */
const YEAR_ZERO_SENTINEL = '0001-01-01T00:00:00Z';
const optionalTimeString = z
  .string()
  .optional()
  .transform((v) => (v === YEAR_ZERO_SENTINEL ? undefined : v));

/**
 * Optional safe-integer schema. Belt-and-braces guard against the v0.2.0
 * audit P1-11 int64 → number precision loss: any Go int64 field exposed via
 * JSON loses precision above 2^53 in JavaScript. The bound below converts
 * silent corruption into a parse error at the wire boundary.
 */
const optionalSafeInt = z
  .number()
  .int()
  .lte(Number.MAX_SAFE_INTEGER)
  .gte(-Number.MAX_SAFE_INTEGER)
  .optional();

/**
 * CORE-047 — closed snapshot enums must degrade, not reject.
 *
 * A strict `z.enum()` rejects the WHOLE snapshot when the daemon emits a value
 * this bundle does not know (a new Go mode/status), which freezes the
 * production dashboard. The single policy for every closed enum on the
 * snapshot wire: `tolerantEnum` parses known values as-is and replaces an
 * unknown one with a documented fallback (the same `.catch()` pattern the
 * dependency-graph enums already used), notifying the fallback listener so
 * the drift is still reported (CORE-048). Lists of enum values
 * (`tolerantEnumList`) drop unknown members instead — a fallback member would
 * invent a permission or capability that was never granted.
 *
 * Form-side schemas (profileForm/automationForm) keep strict enums: there an
 * unknown value is a user-input error, not wire drift.
 */
export type SchemaFallbackListener = (label: string, value: unknown) => void;
let schemaFallbackListener: SchemaFallbackListener | null = null;

/** Registers (or clears, with null) the listener told about every enum fallback. */
export function setSchemaFallbackListener(fn: SchemaFallbackListener | null): void {
  schemaFallbackListener = fn;
}

function noteSchemaFallback(label: string, value: unknown): void {
  try {
    schemaFallbackListener?.(label, value);
  } catch {
    // A reporting failure must never break the parse.
  }
}

function tolerantEnum<const T extends readonly [string, ...string[]]>(
  label: string,
  values: T,
  fallback: T[number],
) {
  return z.enum(values).catch((ctx) => {
    noteSchemaFallback(label, ctx.value);
    return fallback;
  });
}

function tolerantEnumList<const T extends readonly [string, ...string[]]>(
  label: string,
  values: T,
) {
  const known = new Set<string>(values);
  return z.array(z.string()).transform((items) => {
    const kept = items.filter((v) => known.has(v)) as T[number][];
    if (kept.length !== items.length) {
      noteSchemaFallback(
        label,
        items.filter((v) => !known.has(v)),
      );
    }
    return kept;
  });
}

export const CommentRowSchema = z.object({
  author: z.string(),
  body: z.string(),
  createdAt: z.string().optional(), // omitempty — absent when nil
});

export const RunningRowSchema = z.object({
  identifier: z.string(),
  state: z.string(),
  turnCount: z.number(),
  tokens: z.number(),
  inputTokens: z.number(),
  outputTokens: z.number(),
  lastEvent: z.string().optional(), // omitempty — absent before first event
  lastEventAt: z.string().optional(), // omitempty — absent before first event
  sessionId: z.string().optional(), // omitempty — absent until session starts
  workerHost: z.string().optional(), // omitempty — absent for local execution
  backend: z.string().optional(), // omitempty — absent when unknown
  kind: z.string().optional(), // omitempty — "worker" (default) | "reviewer" | "automation"
  subagentCount: z.number().optional(), // omitempty — 0 when no subagents
  elapsedMs: z.number(),
  startedAt: z.string(),
  // Automation context — only set when the run was dispatched by a rule.
  // Manual runs omit both fields entirely (Go side uses `omitempty`).
  automationId: z.string().optional(),
  triggerType: z.string().optional(),
  // Reviewer/comment-count surface (T-6). Absent when zero.
  commentCount: z.number().optional(),
});

export const HistoryRowSchema = z.object({
  identifier: z.string(),
  title: z.string().optional(),
  startedAt: z.string(),
  finishedAt: z.string(),
  elapsedMs: z.number(),
  turnCount: z.number(),
  tokens: z.number(),
  inputTokens: z.number(),
  outputTokens: z.number(),
  // Unknown → 'cancelled': the neutral terminal status (not a red 'failed').
  status: tolerantEnum(
    'history.status',
    ['succeeded', 'failed', 'cancelled', 'stalled', 'input_required'],
    'cancelled',
  ),
  workerHost: z.string().optional(),
  backend: z.string().optional(),
  sessionId: z.string().optional(),
  appSessionId: z.string().optional(),
  kind: z.string().optional(), // omitempty — "worker" (default) | "reviewer" | "automation"
  // Automation context propagated from the live run; absent for manual runs.
  automationId: z.string().optional(),
  triggerType: z.string().optional(),
  commentCount: z.number().optional(),
});

export const RetryRowSchema = z.object({
  identifier: z.string(),
  attempt: z.number(),
  dueAt: z.string(),
  error: z.string().optional(), // omitempty
});

export const CountsSchema = z.object({
  running: z.number(),
  retrying: z.number(),
  paused: z.number(),
});

export const RateLimitInfoSchema = z.object({
  requestsLimit: z.number(),
  requestsRemaining: z.number(),
  requestsReset: z.string().optional(), // omitempty *time.Time — absent when unset
  complexityLimit: z.number().optional(),
  complexityRemaining: z.number().optional(),
});

export const SSHHostInfoSchema = z.object({
  host: z.string(),
  description: z.string().optional(),
});

// Keep in sync with internal/config/agent_actions.go (the Go source of truth).
// A value the daemon emits that is missing here is dropped from the snapshot's
// action lists (tolerantEnumList, CORE-047) rather than rejecting the snapshot.
const AGENT_ACTIONS = [
  'comment',
  'comment_pr',
  'create_issue',
  'merge_pr',
  'move_state',
  'provide_input',
] as const;
// Strict: used by the profile form. Snapshot fields use tolerantEnumList.
export const AllowedAgentActionSchema = z.enum(AGENT_ACTIONS);

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

// CORE-047 round 2: profileDefs round-trip through upsertProfile, so unknown
// allowed actions are kept raw in `unknownAllowedActions` (and re-sent by
// upsertProfile) instead of being dropped by tolerantEnumList.
const PROFILE_KNOWN_KEYS = [
  'command',
  'prompt',
  'soul',
  'instructions',
  'soulFile',
  'instructionsFile',
  'backend',
  'enabled',
  'allowedActions',
  'createIssueState',
];

function captureProfileUnknowns(input: unknown): unknown {
  if (!isRecord(input)) return input;
  if (!Array.isArray(input.allowedActions)) {
    const extra = extraKeys(input, PROFILE_KNOWN_KEYS);
    return extra ? { ...input, unknownKeys: extra } : input;
  }
  const known = new Set<string>(AGENT_ACTIONS);
  const unknown = input.allowedActions.filter(
    (a): a is string => typeof a === 'string' && !known.has(a),
  );
  const out: Record<string, unknown> =
    unknown.length > 0 ? { ...input, unknownAllowedActions: unknown } : { ...input };
  // Keys this bundle does not know at all (z.object would strip them).
  const extra = extraKeys(input, PROFILE_KNOWN_KEYS);
  if (extra) out.unknownKeys = extra;
  return out;
}

const ProfileDefCoreSchema = z.object({
  command: z.string(),
  prompt: z.string().optional(),
  soul: z.string().optional(),
  instructions: z.string().optional(),
  soulFile: z.string().optional(),
  instructionsFile: z.string().optional(),
  backend: z.string().optional(),
  enabled: z.boolean().optional(),
  allowedActions: tolerantEnumList('profileDefs.allowedActions', AGENT_ACTIONS).optional(),
  createIssueState: z.string().optional(),
  // Client-only: raw allowed actions this bundle does not know (round 2).
  unknownAllowedActions: z.array(z.string()).optional(),
  // Client-only: keys this bundle does not know, re-sent by upsertProfile.
  unknownKeys: z.custom<Record<string, JsonValue>>(isRecord).optional(),
});

export const ProfileDefSchema = z.preprocess(captureProfileUnknowns, ProfileDefCoreSchema);

export const ModelOptionSchema = z.object({
  id: z.string(),
  label: z.string(),
});

export const AutomationTriggerSchema = z.object({
  // Unknown → 'cron' for rendering only; the raw trigger is kept in
  // AutomationDef.unknownFields and restored on save.
  type: tolerantEnum('automations.trigger.type', AUTOMATION_TRIGGER_TYPES, 'cron'),
  cron: z.string().optional(),
  timezone: z.string().optional(),
  state: z.string().optional(),
});

export const AutomationFilterSchema = z.object({
  matchMode: tolerantEnum('automations.filter.matchMode', ['all', 'any'], 'all').optional(),
  states: z.array(z.string()).optional(),
  labelsAny: z.array(z.string()).optional(),
  identifierRegex: z.string().optional(),
  limit: z.number().optional(),
  inputContextRegex: z.string().optional(),
  // Gap A — only meaningful on input_required triggers; the server validator
  // rejects it on other types. Skip stale entries (queued > N minutes ago)
  // and drive the dashboard's stale badge.
  maxAgeMinutes: z.number().optional(),
});

export const AutomationPolicySchema = z.object({
  autoResume: z.boolean().optional(),
  // Gap E — rate_limited rules carry these. Server validator enforces:
  //  - switchToProfile required when triggerType === 'rate_limited'
  //  - switchToBackend ∈ {'', 'claude', 'codex'}
  //  - cooldownMinutes >= 0
  // and rejects all three on non-rate_limited triggers.
  switchToProfile: z.string().optional(),
  switchToBackend: tolerantEnum(
    'automations.policy.switchToBackend',
    ['', 'claude', 'codex'],
    '',
  ).optional(),
  cooldownMinutes: z.number().int().nonnegative().optional(),
  moveToState: z.string().optional(),
});

// CORE-047 round 2: the automation list round-trips through
// PUT /settings/automations, so the raw values behind every fallback are kept
// in `unknownFields` and restored by automationDefToWire (configRoundTrip.ts).
const AUTOMATION_KNOWN_KEYS = {
  top: ['id', 'enabled', 'profile', 'instructions', 'trigger', 'filter', 'policy'],
  trigger: Object.keys(AutomationTriggerSchema.shape),
  filter: Object.keys(AutomationFilterSchema.shape),
  policy: Object.keys(AutomationPolicySchema.shape),
} as const;

function captureAutomationUnknowns(input: unknown): unknown {
  if (!isRecord(input)) return input;
  const unknown: AutomationUnknownFields = {};
  const trigger = input.trigger;
  if (isRecord(trigger) && typeof trigger.type === 'string') {
    if (!(AUTOMATION_TRIGGER_TYPES as readonly string[]).includes(trigger.type)) {
      unknown.trigger = { ...trigger } as unknown as RawAutomationTrigger;
      if (isRecord(input.policy)) unknown.policy = { ...input.policy } as RawAutomationPolicy;
    }
  }
  const filter = input.filter;
  if (isRecord(filter) && typeof filter.matchMode === 'string') {
    if (!['all', 'any'].includes(filter.matchMode)) unknown.matchMode = filter.matchMode;
  }
  const policy = input.policy;
  if (isRecord(policy) && typeof policy.switchToBackend === 'string') {
    if (!['', 'claude', 'codex'].includes(policy.switchToBackend)) {
      unknown.switchToBackend = policy.switchToBackend;
    }
  }
  const extra: AutomationExtraKeys = {};
  const top = extraKeys(input, AUTOMATION_KNOWN_KEYS.top);
  if (top) extra.top = top;
  for (const section of ['trigger', 'filter', 'policy'] as const) {
    const obj = input[section];
    // An unknown trigger already keeps its raw trigger and policy whole.
    if (unknown.trigger && section !== 'filter') continue;
    const found = isRecord(obj) ? extraKeys(obj, AUTOMATION_KNOWN_KEYS[section]) : undefined;
    if (found) extra[section] = found;
  }
  if (Object.keys(extra).length > 0) unknown.extra = extra;
  return Object.keys(unknown).length > 0 ? { ...input, unknownFields: unknown } : input;
}

const AutomationDefCoreSchema = z.object({
  id: z.string(),
  enabled: z.boolean(),
  profile: z.string(),
  instructions: z.string().optional(),
  trigger: AutomationTriggerSchema,
  filter: AutomationFilterSchema.optional(),
  policy: AutomationPolicySchema.optional(),
  // Client-only: raw values this bundle does not know (round 2).
  unknownFields: z.custom<AutomationUnknownFields>(isRecord).optional(),
});

export const AutomationDefSchema = z.preprocess(captureAutomationUnknowns, AutomationDefCoreSchema);

// ConfigInvalidStatusSchema mirrors server.ConfigInvalidStatus (Go) — wire
// shape for a current WORKFLOW.md validation failure. The dashboard renders
// a banner when this is present so the operator knows their last edit didn't
// take and the daemon is running on the previously-valid config.
export const ConfigInvalidStatusSchema = z.object({
  path: z.string().optional(),
  error: z.string(),
  retryAttempt: z.number(),
  retryAt: z.string().optional(),
});

export const InputRequiredEntrySchema = z.object({
  identifier: z.string(),
  sessionId: z.string(),
  state: tolerantEnum(
    'inputRequired.state',
    ['input_required', 'pending_input_resume'],
    'input_required',
  ),
  context: z.string(),
  backend: z.string().optional(),
  profile: z.string().optional(),
  queuedAt: z.string(),
  // Gap A: stale + ageMinutes flow from the snapshot path so the dashboard
  // can render a "Stale" badge + age tooltip without re-parsing queuedAt.
  stale: z.boolean().optional(),
  ageMinutes: z.number().optional(),
});

export const AutomationQueueBackpressureSchema = z.object({
  length: z.number(),
  maxLength: z.number(),
  saturated: z.boolean(),
  pausedProducers: z.boolean(),
  rejectedSinceBoot: z.number(),
  lastRejectedAt: optionalTimeString,
  lastRejectedReason: z.string().optional(),
});

// DispatchPressureSchema mirrors server.DispatchPressureRow. It answers
// "would raising max_concurrent_agents help?": slotBoundTicks means yes
// (work was ready and every slot was busy), dependencyBoundTicks means no
// (slots sat idle because the remaining work was blocked).
//
// The two tick counters are mutually exclusive per tick and do NOT sum to
// observedTicks — idle ticks are charged to neither.
export const DispatchPressureSchema = z.object({
  observedTicks: z.number(),
  slotBoundTicks: z.number(),
  dependencyBoundTicks: z.number(),
  utilizationPercent: z.number(),
  // Most-recent-tick counts, unlike the cumulative counters above.
  blockedByDependency: z.number(),
  eligibleWaiting: z.number(),
});

export type DispatchPressure = z.infer<typeof DispatchPressureSchema>;

export const BlockerRefSchema = z.object({
  id: z.string().optional(),
  identifier: z.string().optional(),
  state: z.string().optional(),
  url: z.string().optional(),
});

export const AutomationQueueRowSchema = z.object({
  id: z.string(),
  automationId: z.string(),
  triggerType: z.string(),
  identifier: z.string(),
  title: z.string().optional(),
  issueState: z.string().optional(),
  profile: z.string(),
  backend: z.string().optional(),
  status: tolerantEnum('automationQueue.status', ['queued', 'blocked', 'dispatching'], 'queued'),
  reason: z.string(),
  reasonDetail: z.string().optional(),
  queuedAt: z.string(),
  firedAt: z.string(),
  lastFiredAt: optionalTimeString,
  lastAttemptAt: optionalTimeString,
  attemptCount: z.number(),
  cron: z.string().optional(),
  timezone: z.string().optional(),
  prUrl: z.string().optional(),
  inputContext: z.string().optional(),
  errorMessage: z.string().optional(),
  switchedToProfile: z.string().optional(),
  switchedToBackend: z.string().optional(),
  moveToState: z.string().optional(),
});

export const DependencyAuditRowSchema = z.object({
  identifier: z.string(),
  issueState: z.string(),
  status: tolerantEnum('dependencyAudit.status', ['unknown', 'blocked', 'unblocked'], 'unknown'),
  sources: z.array(z.string()).optional(),
  blockedBy: z.array(BlockerRefSchema).optional(),
  unresolvedBlockers: z.array(BlockerRefSchema).optional(),
  resolvedBlockers: z.array(BlockerRefSchema).optional(),
  wasBlocked: z.boolean(),
  firstBlockedAt: optionalTimeString,
  unblockedAt: optionalTimeString,
  lastAuditedAt: optionalTimeString,
  lastTransitionVersion: optionalSafeInt,
  lastTransitionReason: z.string().optional(),
  degraded: z.boolean().optional(),
});

// DependencyCycleRowSchema mirrors server.DependencyCycleRow (critical-path-
// ordering Task 5/6) — one strongly-connected-component cycle (or self-edge)
// in the tick graph. `kind` follows the same catch-fallback pattern as
// DependencyGraphEdgeSchema.origin above: an unrecognised/future value from a
// newer daemon should not fail the whole snapshot parse, so it falls back to
// 'mixed' (the least-specific classification) rather than throwing.
export const DependencyCycleRowSchema = z.object({
  members: z.array(z.string()),
  kind: tolerantEnum('dependencyCycles.kind', ['tracker', 'inferred', 'mixed'], 'mixed'),
  detectedAt: z.string(),
});

// DependencyAttentionRowSchema mirrors server.DependencyAttentionRow —
// operator-facing dependency alerts: cycle members and issues blocked past
// the configured `dependencies.escalate_blocked_after_hours` window.
export const DependencyAttentionRowSchema = z.object({
  identifier: z.string(),
  blockers: z.array(z.string()),
  blockedSince: z.string(),
  kind: tolerantEnum('dependencyAttention.kind', ['cycle', 'stale_blocker'], 'stale_blocker'),
});

export const DependencyGraphNodeSchema = z.object({
  id: z.string(),
  identifier: z.string(),
  title: z.string().optional(),
  state: z.string().optional(),
  status: tolerantEnum(
    'dependencyGraphNodes.status',
    ['unknown', 'blocked', 'unblocked'],
    'unknown',
  ).optional(),
  running: z.boolean(),
  queued: z.boolean(),
  terminal: z.boolean(),
  updatedAt: z.string().optional(),
  url: z.string().optional(),
});

export const DependencyGraphEdgeSchema = z.object({
  id: z.string(),
  sourceIdentifier: z.string(),
  targetIdentifier: z.string(),
  sourceState: z.string().optional(),
  targetState: z.string().optional(),
  resolved: z.boolean(),
  sourceKnown: z.boolean(),
  // v0.2.0 todolist6 — provenance tag for the inferred-dependency layer.
  // Absent treated as 'tracker' for back-compat with snapshots written before
  // the Phase 2.2 enrichment landed.
  origin: z.enum(['tracker', 'inferred']).optional().catch('tracker'),
  // Inferred edges carry an evidence string from the analyzer agent.
  evidence: z.string().optional(),
  // unified-dependency-graph Task 8 — analyzer confidence score ([0,1]) for
  // an inferred edge. Absent/omitted for tracker edges and for snapshots
  // written before this field existed (old-payload back-compat).
  confidence: z.number().optional(),
  // True when an inferred edge has aged past the configured
  // dependencies.staleness_hours window. Always absent/false for tracker
  // edges.
  stale: z.boolean().optional(),
  // True when an operator has dismissed this issue's inferred blockers via
  // POST/DELETE /api/v1/issues/{identifier}/deps-override.
  overridden: z.boolean().optional(),
  // True when this edge currently blocks dispatch of its target issue,
  // considering confidence/staleness/override/dependencies.inferred_gating.
  gating: z.boolean().optional(),
});

// v0.2.0 todolist6 — Phase 2.3 job-status wire shape returned by
// POST /api/v1/deps/analyze and GET /api/v1/deps/analyze/:jobId.
export const DepsAnalyzeJobSchema = z.object({
  jobId: z.string(),
  profile: z.string().optional(),
  // Unknown → 'failed': a terminal state, so the Cancel affordance and the
  // running spinner never stick on a job this bundle cannot interpret.
  status: tolerantEnum(
    'depsAnalyzeJob.status',
    ['queued', 'running', 'succeeded', 'failed', 'cancelled'],
    'failed',
  ),
  queuedAt: z.string(),
  startedAt: optionalTimeString,
  finishedAt: optionalTimeString,
  issuesScanned: z.number().optional(),
  edgesFound: z.number().optional(),
  chunksTotal: z.number().optional(),
  issuesAnalyzed: z.number().optional(),
  chunksDone: z.number().optional(),
  // Last analyzer progress heartbeat — the only liveness signal on a
  // single-chunk run, where chunksTotal/chunksDone never move.
  lastActivityAt: optionalTimeString,
  error: z.string().optional(),
  // analyzer-autonomy Task 5 — distinguishes an operator-initiated run
  // ("manual" — dashboard button, direct API call, CLI) from a
  // scheduler-initiated one ("auto"). Additive on the wire (server.go's
  // DepsAnalyzeJobRow.Trigger, `omitempty`); an older daemon predating the
  // field, or any unrecognized value, falls back to 'manual' rather than
  // failing the whole job-status parse.
  trigger: tolerantEnum('depsAnalyzeJob.trigger', ['manual', 'auto'], 'manual').optional(),
});

// The POST /api/v1/deps/analyze 202 body — partial shape that overlaps with
// DepsAnalyzeJobSchema but is parsed separately because the server returns
// just {jobId, profile, queuedAt} for the enqueue acknowledgement.
export const DepsAnalyzeEnqueueResponseSchema = z.object({
  jobId: z.string(),
  profile: z.string().optional(),
  queuedAt: z.string(),
});

// OutboxEntryRowSchema mirrors server.OutboxEntryRow (write-ahead-outbox
// design, "Surfaces" / Task 4) — one pending write-ahead-outbox entry as
// exposed on the snapshot for the dashboard's Outbox panel.
export const OutboxEntryRowSchema = z.object({
  id: z.string(),
  kind: z.string(),
  identifier: z.string(),
  targetState: z.string().optional(),
  attempts: z.number(),
  lastError: z.string().optional(),
  degraded: z.boolean().optional(),
  // RateLimitedUntil mirrors server.OutboxEntryRow.RateLimitedUntil
  // (*time.Time, omitempty) — the tracker-published instant this entry is
  // waiting for when the last delivery attempt was deferred by a rate limit.
  rateLimitedUntil: z.string().optional(),
  enqueuedAt: z.string(),
  nextAttemptAt: z.string(),
  // CORE-044 — server.OutboxEntryRow.LastFailedAt (*time.Time, omitempty):
  // when the entry's most recent real delivery failure happened. Absent until
  // the first failure and on snapshots from older daemons.
  lastFailedAt: optionalTimeString,
});

// TrackerErrorRowSchema mirrors server.TrackerErrorRow (CORE-044): the most
// recent tracker failure the event loop observed. op is "poll" or
// "update_state"; kind is "outage" or "rate_limited". Kept as plain strings
// (not enums) so a future op/kind never fails the whole snapshot parse.
/** The Go DepsAnalysisMode constants this bundle knows (parity-tested). */
export const DEPS_ANALYSIS_MODES = ['auto', 'manual'] as const;

export const TrackerErrorRowSchema = z.object({
  at: z.string(),
  op: z.string(),
  kind: z.string(),
  message: z.string(),
  resetAt: optionalTimeString,
  consecutiveFailures: optionalSafeInt,
});

// FailureRowSchema mirrors server.FailureRow (CORE-046): one entry of the
// daemon's RecentFailures ring. kind is worker_failed | worker_stalled |
// tracker_poll | tracker_write | persist | outbox | panic | client, kept as a
// plain string so a future kind never fails the snapshot parse (CORE-047).
// message is redacted daemon-side.
export const TotalsSchema = z.object({
  inputTokens: z.number().int().nonnegative(),
  outputTokens: z.number().int().nonnegative(),
  costUsdEstimated: z.number().nonnegative().nullable(),
  costCoverage: z
    .object({
      claudeRuns: z.number().int().nonnegative(),
      codexRuns: z.number().int().nonnegative(),
    })
    .optional(),
});

export const FailureRowSchema = z.object({
  kind: z.string(),
  identifier: z.string().optional(),
  source: z.string().optional(),
  message: z.string(),
  occurredAt: z.string(),
  recordedAt: z.string(),
  count: z.number().int().nonnegative(),
});

// CORE-055 — BackendHealthRowSchema mirrors server.BackendHealthRow: one
// AGENT backend circuit breaker (CORE-053). Unrelated to rateLimits (the
// tracker API budget) and outboxEntries[].rateLimitedUntil (tracker
// writes). limitedUntil is nullable and always present on the wire: null
// means healthy or "reset unknown" (retryAt then carries the cooldown end).
// An unknown future status degrades to 'warning' — visible, never 'healthy'.
export const BACKEND_HEALTH_STATUSES = ['healthy', 'warning', 'limited', 'probing'] as const;
export const BackendHealthRowSchema = z.object({
  backend: z.string(),
  host: z.string().optional(),
  status: tolerantEnum('backendHealth.status', BACKEND_HEALTH_STATUSES, 'warning'),
  kind: z.string().optional(),
  limitType: z.string().optional(),
  limitedUntil: z.string().nullable().optional(),
  retryAt: optionalTimeString,
  since: optionalTimeString,
  probeIssue: z.string().optional(),
  heldIssues: z.number().int().nonnegative().optional(),
  reroutedIssues: z.number().int().nonnegative().optional(),
});

// CORE-055 — AutoSwitchRowSchema mirrors server.AutoSwitchRow: an issue's
// automatic override (rate_limited automation or backend_fallback) and its
// provenance. Describes the NEXT dispatch, never the running session.
// source is a plain string ("automation" | "backend_fallback" | "unknown").
export const AutoSwitchRowSchema = z.object({
  identifier: z.string(),
  source: z.string(),
  fromBackend: z.string().optional(),
  fromProfile: z.string().optional(),
  toBackend: z.string().optional(),
  toProfile: z.string().optional(),
  reason: z.string().optional(),
  switchedAt: optionalTimeString,
});

export const StateSnapshotSchema = z.object({
  generatedAt: z.string(),
  pollIntervalMs: z.number().optional(), // omitempty — matches Go StateSnapshot.PollIntervalMs
  counts: CountsSchema,
  running: z.array(RunningRowSchema),
  history: z.array(HistoryRowSchema).optional(),
  retrying: z.array(RetryRowSchema),
  paused: z.array(z.string()),
  pausedWithPR: z.record(z.string(), z.string()).optional(),
  // M6-close BH-M6-3 — why each paused issue is paused: 'user_cancelled' |
  // 'user_dismissed_input' | 'retries_exhausted' | 'transition_failed'
  // (open set; unknown values are opaque). Absent on older daemons.
  pauseReasons: z.record(z.string(), z.string()).optional(),
  maxConcurrentAgents: z.number(),
  // G: per-issue retry budget. 0 means "unlimited" (matches Go semantics).
  // Required (no Zod default) per gap §10.3 — a server bug that omits the
  // field should fail loudly at the parse boundary rather than silently
  // defaulting. Test fixtures supply the value (5 matches the Go default).
  maxRetries: z.number(),
  // G: tracker state issues are moved to when retries exhaust.
  // Empty / absent = "Pause (do not move)".
  failedState: z.string().optional(),
  // E: per-issue cap on rate_limited automation switches in a rolling window.
  // 0 = unlimited (operator opt-out). Required (no Zod default) per §10.3.
  maxSwitchesPerIssuePerWindow: z.number(),
  switchWindowHours: z.number(),
  rateLimits: RateLimitInfoSchema.nullable(),
  trackerKind: z.string().optional(),
  activeProjectFilter: z.array(z.string()).optional(),
  projectName: z.string().optional(),
  availableProfiles: z.array(z.string()).optional(),
  profileDefs: z.record(z.string(), ProfileDefSchema).optional(),
  availableModels: z.record(z.string(), z.array(ModelOptionSchema)).optional(),
  supportedAgentActions: tolerantEnumList('supportedAgentActions', AGENT_ACTIONS).optional(),
  reviewerProfile: z.string().optional(),
  autoReview: z.boolean().optional(),
  activeStates: z.array(z.string()).optional(),
  terminalStates: z.array(z.string()).optional(),
  completionState: z.string().optional(),
  // CORE-070 — tracker.working_state (the state an issue is moved to when an
  // agent picks it up). Read-only after startup. Absent from daemons that do
  // not publish it; IssueDetailSlide then falls back to the config default.
  workingState: z.string().optional(),
  backlogStates: z.array(z.string()).optional(),
  autoClearWorkspace: z.boolean().optional(),
  // deps-analysis-mode Task 1/3 — mirrors server.StateSnapshot.DepsAnalysisMode
  // (omitempty on the wire). "auto" | "manual"; absent from snapshots emitted
  // by daemons predating this field, in which case Settings/the Deps tab
  // treat it as "auto" (the pre-existing default behaviour).
  // CORE-047: unknown → 'auto' (the absent-field default) instead of
  // rejecting the whole snapshot.
  depsAnalysisMode: tolerantEnum('depsAnalysisMode', DEPS_ANALYSIS_MODES, 'auto').optional(),
  currentAppSessionId: z.string().optional(),
  sshHosts: z.array(SSHHostInfoSchema).optional(),
  dispatchStrategy: z.string().optional(),
  defaultBackend: z.string().optional(),
  inlineInput: z.boolean().optional(),
  automations: z.array(AutomationDefSchema).optional(),
  inputRequired: z.array(InputRequiredEntrySchema).optional(),
  automationQueue: z.array(AutomationQueueRowSchema).optional(),
  automationQueueBackpressure: AutomationQueueBackpressureSchema.optional(),
  // Go-side omitempty: absent (not a zero row) until the daemon completes
  // its first tick, and absent from snapshots emitted by older daemons.
  dispatchPressure: DispatchPressureSchema.optional(),
  // gaps_11 G-11 — monotonic count of input_required automation dispatches
  // suppressed by the self-reentry guard. Go-side omitempty: absent (not 0)
  // until the first drop, and absent from snapshots emitted by older daemons.
  automationDropsSelfReentryTotal: optionalSafeInt,
  dependencyAudit: z.array(DependencyAuditRowSchema).optional(),
  // depsRefreshInFlight is the off-loop refresher's current batch size, not
  // a boolean latch — 0/absent means idle (or a states-only batch with no
  // row targets; see LiveOpsStrip's depsChipLabel for why that case renders
  // no "refreshing" suffix rather than "refreshing 0").
  depsRefreshInFlight: optionalSafeInt,
  // depsRefreshLastDurationMs is the wall-clock of the last completed
  // refresh batch. int64 on the wire, hence optionalSafeInt (not a plain
  // z.number()) per the v0.2.0 audit P1-11 precision guard above.
  depsRefreshLastDurationMs: optionalSafeInt,
  // depsRefreshDegradedCount is how many dependency-audit rows are past the
  // orchestrator's consecutive-failure threshold.
  depsRefreshDegradedCount: optionalSafeInt,
  dependencyGraphNodes: z.array(DependencyGraphNodeSchema).optional(),
  dependencyGraphEdges: z.array(DependencyGraphEdgeSchema).optional(),
  // v0.2.0 todolist6 — gates the dashboard "Analyze dependencies" button.
  // Absent / empty disables the button. The frontend ALSO checks that the
  // named profile exists in profileDefs and is enabled before enabling the
  // button — empty here is the only daemon-side gate.
  depsAnalyzerProfile: z.string().optional(),
  // v0.2.0 todolist6 — surfaced as a "Last analyzed N ago" label on the Deps
  // toolbar. Absent means the sidecar has never been written; the label
  // reads "Never" in that case.
  depsLastAnalyzedAt: optionalTimeString.optional(),
  // #46-1 — the analyzer JobManager's CURRENT job (running or most recently
  // terminal), independent of which browser tab/click started it. Reuses
  // DepsAnalyzeJobSchema (the GET /api/v1/deps/analyze/:jobId shape) so a
  // page refresh/navigation during a running job can still derive the
  // Cancel affordance and the passive "auto" badge from server state
  // instead of mutation-local frontend state, which dies on reload. Absent
  // when no analyzer job has ever run this daemon process lifetime.
  depsAnalyzeJob: DepsAnalyzeJobSchema.optional(),
  // ConfigInvalid surfaces a failed WORKFLOW.md reload to the banner. Absent
  // when the daemon is reading a valid config; present (non-null) when the
  // most recent reload tick failed and the daemon is exponentially backing
  // off retries on the last-valid config (T-26).
  configInvalid: ConfigInvalidStatusSchema.optional(),
  // CORE-044 — most recent tracker failure (poll outage/rate limit, or a
  // failed failed-state move). Absent when none is recorded and on snapshots
  // from daemons predating the field.
  lastTrackerError: TrackerErrorRowSchema.optional(),
  // CORE-046 — bounded ring of recent failures, oldest recorded first.
  // Always an array on a current daemon; absent on older daemons.
  recentFailures: z.array(FailureRowSchema).optional(),
  // CORE-091 — daemon-session cumulative token and estimated-cost totals
  // (reset on daemon restart, not persisted). costUsdEstimated is null until a
  // Claude run reports total_cost_usd; Codex reports no cost, so
  // costCoverage says which runs the estimate covers. Optional: daemons
  // without the Go half (M6-W3 handoff) omit it and the tile hides.
  totals: TotalsSchema.optional(),
  // CORE-175 — optional daemon features the dashboard may use; an absent
  // entry hides the matching UI (older daemons omit the whole field).
  capabilities: z.array(z.string()).optional(),
  // CORE-175 — per-issue failure acknowledgements (event-loop state): the
  // attention inbox hides worker_failed/stalled entries with occurredAt <= upTo.
  failureAcks: z.array(z.object({ identifier: z.string(), upTo: z.string() })).optional(),
  // critical-path-ordering Task 4/5/6 — this tick's cycle-detection output
  // and derived operator-attention entries. Both omitempty on the wire:
  // absent on snapshots from daemons predating this feature, and on ticks
  // with no cycles / no attention-worthy blockers.
  dependencyCycles: z.array(DependencyCycleRowSchema).optional(),
  dependencyAttention: z.array(DependencyAttentionRowSchema).optional(),
  // outbox Task 4 — write-ahead-outbox surfaces. outboxEntries is this
  // tick's outbox contents in global enqueue order; outboxSyncing is the
  // sorted join-key list (issue identifiers) the frontend matches against
  // /api/v1/issues rows by identifier to render the "syncing" badge —
  // TrackerIssue has no Syncing field of its own (see
  // server.StateSnapshot.OutboxSyncing's doc comment). Both additive/
  // omitempty on the wire: absent on snapshots from daemons predating this
  // feature.
  outboxEntries: z.array(OutboxEntryRowSchema).optional(),
  outboxSyncing: z.array(z.string()).optional(),
  // CORE-055 — agent backend breakers and auto-switch provenance. Both
  // optional/omitempty: absent on daemons predating CORE-053/055.
  backendHealth: z.array(BackendHealthRowSchema).optional(),
  autoSwitches: z.array(AutoSwitchRowSchema).optional(),
});

export const LogEventTypeSchema = z.enum([
  'text',
  'action',
  'subagent',
  'pr',
  'turn',
  'warn',
  'info',
  'error',
  // Surfaces the orchestrator's AUTOMATION FIRED log block in the per-issue
  // timeline so operators can confirm dispatch decisions.
  'automation',
]);

export const IssueLogEntrySchema = z.object({
  level: z.string(),
  // codex-B5: `info` remains the catch fallback because the orchestrator's
  // text logging defaults to `info` for any unstructured line — preserving
  // that as the parse sentinel means a future trailing line without a
  // recognised event tag renders as a plain info entry instead of being
  // dropped from the per-issue log view.
  event: LogEventTypeSchema.catch('info'),
  message: z.string(),
  tool: z.string().optional(),
  time: z.string().optional(),
  detail: z.string().optional(),
  sessionId: z.string().optional(),
});

export const BlockerDetailSchema = z.object({
  identifier: z.string(),
  state: z.string().optional(),
  url: z.string().optional(),
});

export const IssueStatusChangeSchema = z.object({
  fromState: z.string().optional(),
  toState: z.string(),
  source: z.string(),
  automationId: z.string().optional(),
  triggerType: z.string().optional(),
  profileName: z.string().optional(),
  backend: z.string().optional(),
  workerHost: z.string().optional(),
  at: z.string(),
});

export const TrackerIssueSchema = z.object({
  identifier: z.string(),
  title: z.string(),
  state: z.string(),
  description: z.string().optional(), // omitempty — absent when ""
  url: z.string().optional(), // omitempty — absent when ""
  orchestratorState: tolerantEnum(
    'issues.orchestratorState',
    ['idle', 'running', 'retrying', 'paused', 'input_required', 'pending_input_resume'],
    'idle',
  ),
  turnCount: z.number().optional(), // omitempty — absent when 0
  tokens: z.number().optional(), // omitempty — absent when 0
  elapsedMs: z.number().optional(), // omitempty — absent when 0
  lastMessage: z.string().optional(), // omitempty — absent when ""
  error: z.string().optional(), // omitempty — absent when ""
  labels: z.array(z.string()).optional(),
  priority: z.number().nullable().optional(),
  branchName: z.string().nullable().optional(),
  blockedBy: z.array(z.string()).optional(),
  blockedByDetails: z.array(BlockerDetailSchema).optional(),
  comments: z.array(CommentRowSchema).optional(),
  statusChanges: z.array(IssueStatusChangeSchema).optional(),
  ineligibleReason: z.string().optional(),
  agentProfile: z.string().optional(),
  agentBackend: z.string().optional(),
  // CORE-055 — set when agentProfile/agentBackend come from an automatic
  // switch rather than an operator pin.
  autoSwitch: AutoSwitchRowSchema.optional(),
});

// Inferred TypeScript types — re-exported from itervox.ts for backward compatibility.
export type SSHHostInfo = z.infer<typeof SSHHostInfoSchema>;
export type CommentRow = z.infer<typeof CommentRowSchema>;
export type RunningRow = z.infer<typeof RunningRowSchema>;
export type HistoryRow = z.infer<typeof HistoryRowSchema>;
export type RetryRow = z.infer<typeof RetryRowSchema>;
export type Counts = z.infer<typeof CountsSchema>;
export type RateLimitInfo = z.infer<typeof RateLimitInfoSchema>;
export type Totals = z.infer<typeof TotalsSchema>;
export type ProfileDef = z.infer<typeof ProfileDefSchema>;
export type AutomationDef = z.infer<typeof AutomationDefSchema>;
export type StateSnapshot = z.infer<typeof StateSnapshotSchema>;
export type BackendHealthRow = z.infer<typeof BackendHealthRowSchema>;
export type AutoSwitchRow = z.infer<typeof AutoSwitchRowSchema>;
export type LogEventType = z.infer<typeof LogEventTypeSchema>;
export type IssueLogEntry = z.infer<typeof IssueLogEntrySchema>;
export type BlockerDetail = z.infer<typeof BlockerDetailSchema>;
export type IssueStatusChange = z.infer<typeof IssueStatusChangeSchema>;
export type TrackerIssue = z.infer<typeof TrackerIssueSchema>;
export type InputRequiredEntry = z.infer<typeof InputRequiredEntrySchema>;
export type AutomationQueueRow = z.infer<typeof AutomationQueueRowSchema>;
export type AutomationQueueBackpressure = z.infer<typeof AutomationQueueBackpressureSchema>;
export type DependencyAuditRow = z.infer<typeof DependencyAuditRowSchema>;
export type DependencyGraphNode = z.infer<typeof DependencyGraphNodeSchema>;
export type DependencyGraphEdge = z.infer<typeof DependencyGraphEdgeSchema>;
export type DependencyCycleRow = z.infer<typeof DependencyCycleRowSchema>;
export type DependencyAttentionRow = z.infer<typeof DependencyAttentionRowSchema>;
export type DepsAnalyzeJob = z.infer<typeof DepsAnalyzeJobSchema>;
export type DepsAnalyzeEnqueueResponse = z.infer<typeof DepsAnalyzeEnqueueResponseSchema>;
export type ConfigInvalidStatus = z.infer<typeof ConfigInvalidStatusSchema>;
export type OutboxEntryRow = z.infer<typeof OutboxEntryRowSchema>;
export type FailureRow = z.infer<typeof FailureRowSchema>;

// --- Skills inventory (T-89) ---
//
// Mirrors the Go types in `internal/skills/types.go`. The Go side encodes via
// the default `json` tags (PascalCase → camelCase via library convention is
// NOT applied; encoding/json keeps PascalCase by default). We mirror that
// here so .parse() round-trips a daemon JSON response unchanged.

export const SkillSchema = z.object({
  Name: z.string(),
  Description: z.string().optional(),
  Provider: z.string(),
  Source: z.string(),
  FilePath: z.string().optional(),
  ApproxTokens: z.number(),
  TriggerPatterns: z.array(z.string()).nullable().optional(),
});

// Claude Code subagent from .claude/agents (project / user) or a plugin (#86).
export const SubagentSchema = z.object({
  Name: z.string(),
  Description: z.string().optional(),
  Tools: z.array(z.string()).nullable().optional(),
  Model: z.string().optional(),
  Provider: z.string(),
  Source: z.string(),
  FilePath: z.string().optional(),
  ApproxTokens: z.number(),
});

export const InstructionDocSchema = z.object({
  Name: z.string(),
  Provider: z.string(),
  Scope: z.string(),
  FilePath: z.string(),
  ApproxTokens: z.number(),
});

export const HookEntrySchema = z.object({
  Event: z.string(),
  Matcher: z.string().optional(),
  Command: z.string(),
  Provider: z.string(),
  Source: z.string(),
  ApproxTokens: z.number(),
});

export const MCPServerSchema = z.object({
  Name: z.string(),
  Transport: z.string().optional(),
  Command: z.string().optional(),
  URL: z.string().optional(),
  Source: z.string(),
  Tools: z.array(z.string()).nullable().optional(),
});

export const PluginSchema = z.object({
  Name: z.string(),
  Provider: z.string(),
  FilePath: z.string().optional(),
  Source: z.string(),
  ApproxTokens: z.number(),
  Skills: z.array(SkillSchema).nullable().optional(),
  Hooks: z.array(HookEntrySchema).nullable().optional(),
  Agents: z
    .array(
      z.object({
        Name: z.string(),
        Description: z.string().optional(),
        FilePath: z.string().optional(),
      }),
    )
    .nullable()
    .optional(),
  Commands: z
    .array(
      z.object({
        Name: z.string(),
        Description: z.string().optional(),
        FilePath: z.string().optional(),
      }),
    )
    .nullable()
    .optional(),
});

export const InventoryFixSchema = z.object({
  Label: z.string(),
  Action: z.string(),
  Target: z.string().optional(),
  Destructive: z.boolean(),
});

export const InventoryIssueSchema = z.object({
  ID: z.string(),
  Severity: z.string(),
  Title: z.string(),
  Description: z.string(),
  Affected: z.array(z.string()).nullable().optional(),
  Fix: InventoryFixSchema.nullable().optional(),
});

export const InventorySchema = z.object({
  ScanTime: z.string(),
  Partial: z.boolean().optional(),
  ScanError: z.string().optional(),
  Stale: z.boolean().optional(),
  Skills: z.array(SkillSchema).nullable().optional(),
  Subagents: z.array(SubagentSchema).nullable().optional(),
  Plugins: z.array(PluginSchema).nullable().optional(),
  MCPServers: z.array(MCPServerSchema).nullable().optional(),
  Hooks: z.array(HookEntrySchema).nullable().optional(),
  Instructions: z.array(InstructionDocSchema).nullable().optional(),
  Issues: z.array(InventoryIssueSchema).nullable().optional(),
  // Other fields intentionally omitted — added when the corresponding
  // Phase-2/Phase-3 features land in the frontend.
});

export type Skill = z.infer<typeof SkillSchema>;
export type Subagent = z.infer<typeof SubagentSchema>;
export type InstructionDocEntry = z.infer<typeof InstructionDocSchema>;
export type HookEntry = z.infer<typeof HookEntrySchema>;
export type MCPServer = z.infer<typeof MCPServerSchema>;
export type SkillsPlugin = z.infer<typeof PluginSchema>;
export type InventoryIssue = z.infer<typeof InventoryIssueSchema>;
export type InventoryFix = z.infer<typeof InventoryFixSchema>;
export type SkillsInventory = z.infer<typeof InventorySchema>;

// --- Skills analytics (T-100..T-104) ---

export const CapabilityStatSchema = z.object({
  CapabilityID: z.string(),
  Uses: z.number().optional(),
  RuntimeLoads: z.number().optional(),
  ApproxTokens: z.number().optional(),
  LastSeenAt: z.string().nullable().optional(),
  Configured: z.boolean().optional(),
  RuntimeVerified: z.boolean().optional(),
});

export const ProfileCostSchema = z.object({
  ProfileName: z.string(),
  TotalApproxTokens: z.number().optional(),
  InstructionTokens: z.number().optional(),
  SkillTokens: z.number().optional(),
  HookTokens: z.number().optional(),
  MCPToolSchemaTokens: z.number().optional(),
  WorkflowTemplateTokens: z.number().optional(),
});

export const RecommendationSchema = z.object({
  ID: z.string(),
  Severity: z.string(),
  Category: z.string().optional(),
  Title: z.string(),
  Description: z.string(),
  Affected: z.array(z.string()).nullable().optional(),
});

export const AnalyticsSnapshotSchema = z.object({
  GeneratedAt: z.string(),
  HasRuntimeEvidence: z.boolean().optional(),
  SkillStats: z.array(CapabilityStatSchema).nullable().optional(),
  HookStats: z.array(CapabilityStatSchema).nullable().optional(),
  ProfileCosts: z.array(ProfileCostSchema).nullable().optional(),
  Recommendations: z.array(RecommendationSchema).nullable().optional(),
});

export type CapabilityStat = z.infer<typeof CapabilityStatSchema>;
export type ProfileCost = z.infer<typeof ProfileCostSchema>;
export type Recommendation = z.infer<typeof RecommendationSchema>;
export type AnalyticsSnapshotData = z.infer<typeof AnalyticsSnapshotSchema>;
