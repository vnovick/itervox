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
  status: z.enum(['succeeded', 'failed', 'cancelled', 'stalled', 'input_required']),
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

export const AllowedAgentActionSchema = z.enum([
  'comment',
  'comment_pr',
  'create_issue',
  'move_state',
  'provide_input',
]);

export const ProfileDefSchema = z.object({
  command: z.string(),
  prompt: z.string().optional(),
  soul: z.string().optional(),
  instructions: z.string().optional(),
  soulFile: z.string().optional(),
  instructionsFile: z.string().optional(),
  backend: z.string().optional(),
  enabled: z.boolean().optional(),
  allowedActions: z.array(AllowedAgentActionSchema).optional(),
  createIssueState: z.string().optional(),
});

export const ModelOptionSchema = z.object({
  id: z.string(),
  label: z.string(),
});

export const AutomationTriggerSchema = z.object({
  type: z.enum(AUTOMATION_TRIGGER_TYPES),
  cron: z.string().optional(),
  timezone: z.string().optional(),
  state: z.string().optional(),
});

export const AutomationFilterSchema = z.object({
  matchMode: z.enum(['all', 'any']).optional(),
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
  switchToBackend: z.enum(['', 'claude', 'codex']).optional(),
  cooldownMinutes: z.number().int().nonnegative().optional(),
  moveToState: z.string().optional(),
});

export const AutomationDefSchema = z.object({
  id: z.string(),
  enabled: z.boolean(),
  profile: z.string(),
  instructions: z.string().optional(),
  trigger: AutomationTriggerSchema,
  filter: AutomationFilterSchema.optional(),
  policy: AutomationPolicySchema.optional(),
});

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
  state: z.enum(['input_required', 'pending_input_resume']),
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
  status: z.enum(['queued', 'blocked', 'dispatching']),
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
  status: z.enum(['unknown', 'blocked', 'unblocked']),
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
});

export const DependencyGraphNodeSchema = z.object({
  id: z.string(),
  identifier: z.string(),
  title: z.string().optional(),
  state: z.string().optional(),
  status: z.enum(['unknown', 'blocked', 'unblocked']).optional(),
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
});

// v0.2.0 todolist6 — Phase 2.3 job-status wire shape returned by
// POST /api/v1/deps/analyze and GET /api/v1/deps/analyze/:jobId.
export const DepsAnalyzeJobSchema = z.object({
  jobId: z.string(),
  profile: z.string().optional(),
  status: z.enum(['queued', 'running', 'succeeded', 'failed']),
  queuedAt: z.string(),
  startedAt: optionalTimeString,
  finishedAt: optionalTimeString,
  issuesScanned: z.number().optional(),
  edgesFound: z.number().optional(),
  error: z.string().optional(),
});

// The POST /api/v1/deps/analyze 202 body — partial shape that overlaps with
// DepsAnalyzeJobSchema but is parsed separately because the server returns
// just {jobId, profile, queuedAt} for the enqueue acknowledgement.
export const DepsAnalyzeEnqueueResponseSchema = z.object({
  jobId: z.string(),
  profile: z.string().optional(),
  queuedAt: z.string(),
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
  supportedAgentActions: z.array(AllowedAgentActionSchema).optional(),
  reviewerProfile: z.string().optional(),
  autoReview: z.boolean().optional(),
  activeStates: z.array(z.string()).optional(),
  terminalStates: z.array(z.string()).optional(),
  completionState: z.string().optional(),
  backlogStates: z.array(z.string()).optional(),
  autoClearWorkspace: z.boolean().optional(),
  currentAppSessionId: z.string().optional(),
  sshHosts: z.array(SSHHostInfoSchema).optional(),
  dispatchStrategy: z.string().optional(),
  defaultBackend: z.string().optional(),
  inlineInput: z.boolean().optional(),
  automations: z.array(AutomationDefSchema).optional(),
  inputRequired: z.array(InputRequiredEntrySchema).optional(),
  automationQueue: z.array(AutomationQueueRowSchema).optional(),
  automationQueueBackpressure: AutomationQueueBackpressureSchema.optional(),
  dependencyAudit: z.array(DependencyAuditRowSchema).optional(),
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
  // ConfigInvalid surfaces a failed WORKFLOW.md reload to the banner. Absent
  // when the daemon is reading a valid config; present (non-null) when the
  // most recent reload tick failed and the daemon is exponentially backing
  // off retries on the last-valid config (T-26).
  configInvalid: ConfigInvalidStatusSchema.optional(),
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
  // v0.2.0 todolist5 B5 — surfaces the orchestrator's AUTOMATION FIRED log block
  // in the per-issue timeline so operators can confirm dispatch decisions.
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
  orchestratorState: z.enum([
    'idle',
    'running',
    'retrying',
    'paused',
    'input_required',
    'pending_input_resume',
  ]),
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
});

// Inferred TypeScript types — re-exported from itervox.ts for backward compatibility.
export type SSHHostInfo = z.infer<typeof SSHHostInfoSchema>;
export type CommentRow = z.infer<typeof CommentRowSchema>;
export type RunningRow = z.infer<typeof RunningRowSchema>;
export type HistoryRow = z.infer<typeof HistoryRowSchema>;
export type RetryRow = z.infer<typeof RetryRowSchema>;
export type Counts = z.infer<typeof CountsSchema>;
export type RateLimitInfo = z.infer<typeof RateLimitInfoSchema>;
export type ProfileDef = z.infer<typeof ProfileDefSchema>;
export type AutomationDef = z.infer<typeof AutomationDefSchema>;
export type StateSnapshot = z.infer<typeof StateSnapshotSchema>;
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
export type DepsAnalyzeJob = z.infer<typeof DepsAnalyzeJobSchema>;
export type DepsAnalyzeEnqueueResponse = z.infer<typeof DepsAnalyzeEnqueueResponseSchema>;
export type ConfigInvalidStatus = z.infer<typeof ConfigInvalidStatusSchema>;

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
  Plugins: z.array(PluginSchema).nullable().optional(),
  MCPServers: z.array(MCPServerSchema).nullable().optional(),
  Hooks: z.array(HookEntrySchema).nullable().optional(),
  Instructions: z.array(InstructionDocSchema).nullable().optional(),
  Issues: z.array(InventoryIssueSchema).nullable().optional(),
  // Other fields intentionally omitted — added when the corresponding
  // Phase-2/Phase-3 features land in the frontend.
});

export type Skill = z.infer<typeof SkillSchema>;
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
