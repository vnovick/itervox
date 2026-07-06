// Snapshot fixture factories. Every factory ends with `Schema.parse(...)` so a
// schema drift in `web/src/types/schemas.ts` (driven by a Go-side change) fails
// the fixture test on construction, not at the call site.

import {
  AutomationDefSchema,
  ConfigInvalidStatusSchema,
  CountsSchema,
  HistoryRowSchema,
  InputRequiredEntrySchema,
  ProfileDefSchema,
  RetryRowSchema,
  RunningRowSchema,
  SSHHostInfoSchema,
  StateSnapshotSchema,
  type AutomationDef,
  type ConfigInvalidStatus,
  type Counts,
  type HistoryRow,
  type InputRequiredEntry,
  type ProfileDef,
  type RetryRow,
  type RunningRow,
  type SSHHostInfo,
  type StateSnapshot,
} from '../../types/schemas';
import { applyOverrides, type DeepPartial } from './deepPartial';
import { BASE_TIME, formatRFC3339, minutesAgo, secondsAgo } from './time';

// Backwards-compatible alias for the older name used in earlier drafts.
export type ConfigInvalid = ConfigInvalidStatus;

export function makeRunningRow(overrides?: DeepPartial<RunningRow>): RunningRow {
  const base: RunningRow = {
    identifier: 'DEMO-1',
    state: 'In Progress',
    turnCount: 3,
    tokens: 4200,
    inputTokens: 3800,
    outputTokens: 400,
    lastEvent: 'tool_use:Read',
    lastEventAt: formatRFC3339(secondsAgo(15)),
    sessionId: 'sess-demo-1',
    backend: 'local',
    kind: 'worker',
    subagentCount: 0,
    elapsedMs: 30_000,
    startedAt: formatRFC3339(minutesAgo(2)),
  };
  return RunningRowSchema.parse(applyOverrides(base, overrides));
}

export function makeHistoryRow(overrides?: DeepPartial<HistoryRow>): HistoryRow {
  const base: HistoryRow = {
    identifier: 'DEMO-9',
    title: 'History row title',
    startedAt: formatRFC3339(minutesAgo(45)),
    finishedAt: formatRFC3339(minutesAgo(30)),
    elapsedMs: 15 * 60 * 1000,
    turnCount: 12,
    tokens: 18_000,
    inputTokens: 16_000,
    outputTokens: 2_000,
    status: 'succeeded',
    backend: 'local',
    sessionId: 'sess-demo-9',
    kind: 'worker',
  };
  return HistoryRowSchema.parse(applyOverrides(base, overrides));
}

export function makeRetryRow(overrides?: DeepPartial<RetryRow>): RetryRow {
  const base: RetryRow = {
    identifier: 'DEMO-3',
    attempt: 1,
    dueAt: formatRFC3339(new Date(BASE_TIME.getTime() + 30 * 1000)),
    error: 'rate_limit: try again in 30s',
  };
  return RetryRowSchema.parse(applyOverrides(base, overrides));
}

export function makeInputRequiredRow(
  overrides?: DeepPartial<InputRequiredEntry>,
): InputRequiredEntry {
  const base: InputRequiredEntry = {
    identifier: 'DEMO-INPUT-1',
    sessionId: 'sess-input-1',
    state: 'input_required',
    context: 'Need confirmation before running migration.',
    backend: 'local',
    profile: 'default',
    queuedAt: formatRFC3339(minutesAgo(1)),
  };
  return InputRequiredEntrySchema.parse(applyOverrides(base, overrides));
}

export function makePendingInputResumeRow(
  overrides?: DeepPartial<InputRequiredEntry>,
): InputRequiredEntry {
  const base: InputRequiredEntry = {
    identifier: 'DEMO-INPUT-2',
    sessionId: 'sess-input-2',
    state: 'pending_input_resume',
    context: 'User input received; resuming next tick.',
    backend: 'local',
    profile: 'default',
    queuedAt: formatRFC3339(secondsAgo(10)),
  };
  return InputRequiredEntrySchema.parse(applyOverrides(base, overrides));
}

export function makeConfigInvalidStatus(overrides?: DeepPartial<ConfigInvalid>): ConfigInvalid {
  const base: ConfigInvalid = {
    path: 'WORKFLOW.md',
    error: 'unknown_field: agent.weird_field',
    retryAttempt: 1,
    retryAt: formatRFC3339(new Date(BASE_TIME.getTime() + 5 * 1000)),
  };
  return ConfigInvalidStatusSchema.parse(applyOverrides(base, overrides));
}

export function makeProfileDef(overrides?: DeepPartial<ProfileDef>): ProfileDef {
  const base: ProfileDef = {
    command: 'claude',
    prompt: 'Default prompt body.',
    soul: '# Default SOUL',
    instructions: 'Default prompt body.',
    soulFile: '.itervox/agents/default/SOUL.md',
    instructionsFile: '.itervox/agents/default/INSTRUCTIONS.md',
    backend: 'local',
    enabled: true,
    allowedActions: ['comment', 'move_state', 'provide_input'],
  };
  return ProfileDefSchema.parse(applyOverrides(base, overrides));
}

export function makeAutomation(overrides?: DeepPartial<AutomationDef>): AutomationDef {
  const base: AutomationDef = {
    id: 'auto-1',
    enabled: true,
    profile: 'default',
    instructions: 'Run nightly cleanup.',
    trigger: { type: 'cron', cron: '0 3 * * *', timezone: 'UTC' },
    filter: { matchMode: 'all', states: ['Backlog'] },
    policy: { autoResume: false },
  };
  return AutomationDefSchema.parse(applyOverrides(base, overrides));
}

export function makeSSHHostInfo(overrides?: DeepPartial<SSHHostInfo>): SSHHostInfo {
  const base: SSHHostInfo = {
    host: 'remote-1.example.com',
    description: 'Primary build worker.',
  };
  return SSHHostInfoSchema.parse(applyOverrides(base, overrides));
}

function deriveCounts(snap: {
  running: RunningRow[];
  retrying: RetryRow[];
  paused: string[];
}): Counts {
  return CountsSchema.parse({
    running: snap.running.length,
    retrying: snap.retrying.length,
    paused: snap.paused.length,
  });
}

// Most fixtures want to replace these arrays wholesale; declared explicitly so
// callers get array (not deep-merged-array) ergonomics. Using `Omit` instead
// of `extends` avoids the conflict between DeepPartial<RunningRow[]> from the
// parent and the explicit RunningRow[] override here.
export type MakeSnapshotOverrides = Omit<
  DeepPartial<StateSnapshot>,
  'running' | 'retrying' | 'history' | 'paused'
> & {
  running?: RunningRow[];
  retrying?: RetryRow[];
  history?: HistoryRow[];
  paused?: string[];
};

export function makeSnapshot(overrides?: MakeSnapshotOverrides): StateSnapshot {
  const baseRunning: RunningRow[] = [];
  const baseRetrying: RetryRow[] = [];
  const basePaused: string[] = [];

  // Two-phase: build minimum viable snapshot, then re-derive `counts` from
  // the (possibly overridden) arrays unless caller explicitly set counts.
  const running = overrides?.running ?? baseRunning;
  const retrying = overrides?.retrying ?? baseRetrying;
  const paused = overrides?.paused ?? basePaused;

  const base: StateSnapshot = {
    generatedAt: formatRFC3339(BASE_TIME),
    pollIntervalMs: 1500,
    counts: deriveCounts({ running, retrying, paused }),
    running,
    history: overrides?.history ?? [],
    retrying,
    paused,
    pausedWithPR: {},
    maxConcurrentAgents: 3,
    // G + E — match the Go-side defaults so fixtures parse against the
    // tightened TypeScript types (Zod parses with defaults, but raw types
    // require these fields).
    maxRetries: 5,
    maxSwitchesPerIssuePerWindow: 2,
    switchWindowHours: 6,
    rateLimits: null,
    trackerKind: 'memory',
    activeProjectFilter: [],
    projectName: 'Demo Project',
    availableProfiles: ['default'],
    profileDefs: { default: makeProfileDef() },
    availableModels: {},
    reviewerProfile: '',
    autoReview: false,
    activeStates: ['In Progress'],
    terminalStates: ['Done'],
    completionState: 'Done',
    backlogStates: ['Backlog'],
    autoClearWorkspace: false,
    currentAppSessionId: 'app-sess-fixture',
    sshHosts: [],
    dispatchStrategy: 'fifo',
    defaultBackend: 'local',
    inlineInput: false,
    automations: [],
    inputRequired: [],
  };

  // Pull running/retrying/paused/history out of overrides so applyOverrides
  // doesn't re-process them as deep-merge candidates. Using explicit filtering
  // (instead of destructure + rest) avoids TS narrowing pitfalls with the
  // Omit-based MakeSnapshotOverrides shape.
  const REPLACED_KEYS: ReadonlyArray<string> = ['running', 'retrying', 'history', 'paused'];
  const rest: Record<string, unknown> = {};
  if (overrides) {
    for (const [k, v] of Object.entries(overrides)) {
      if (!REPLACED_KEYS.includes(k)) rest[k] = v;
    }
  }

  const merged = applyOverrides<StateSnapshot>(base, rest as DeepPartial<StateSnapshot>);

  // If caller did not override counts, re-derive from arrays (running etc may
  // have changed via overrides).
  if (!overrides?.counts) {
    merged.counts = deriveCounts({
      running: merged.running,
      retrying: merged.retrying,
      paused: merged.paused,
    });
  }
  return StateSnapshotSchema.parse(merged);
}
