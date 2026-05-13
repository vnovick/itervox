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
  type: z.enum([
    'cron',
    'input_required',
    'tracker_comment_added',
    'issue_entered_state',
    'issue_moved_to_backlog',
    'run_failed',
    'pr_opened',
    'rate_limited',
  ]),
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
]);

export const IssueLogEntrySchema = z.object({
  level: z.string(),
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
export type TrackerIssue = z.infer<typeof TrackerIssueSchema>;
export type InputRequiredEntry = z.infer<typeof InputRequiredEntrySchema>;
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
  SkillStats: z.array(CapabilityStatSchema).nullable().optional(),
  HookStats: z.array(CapabilityStatSchema).nullable().optional(),
  ProfileCosts: z.array(ProfileCostSchema).nullable().optional(),
  Recommendations: z.array(RecommendationSchema).nullable().optional(),
});

export type CapabilityStat = z.infer<typeof CapabilityStatSchema>;
export type ProfileCost = z.infer<typeof ProfileCostSchema>;
export type Recommendation = z.infer<typeof RecommendationSchema>;
export type AnalyticsSnapshotData = z.infer<typeof AnalyticsSnapshotSchema>;
