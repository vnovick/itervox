// Route-mocked data for the visual tour's Rules, Memory, Repo Analysis,
// Settings (access tokens) and hub stops, so their screenshots show filled
// pages instead of loading states. Install with `installTourData(api)` after
// bootApp / installMockApi; each route is an override, so other specs keep
// the default empty stubs.

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { MockApiHandle } from './mockApi';

const T = '2026-10-01T12:00:00Z';

function json(body: unknown) {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) };
}

const RULES = {
  schemaUrl: 'https://itervox.dev/schema/rules.schema.json',
  layers: [
    {
      name: 'user',
      path: '/home/me/.itervox/rules.json',
      writable: true,
      exists: false,
      raw: '',
      version: '',
      rules: [],
    },
    {
      name: 'project',
      path: '/repo/.itervox/rules.json',
      writable: true,
      exists: true,
      raw: '{"rules": []}',
      version: 'v1',
      rules: [
        { id: 'pnpm', severity: 'must', text: 'Use pnpm.' },
        { id: 'no-token-logs', severity: 'must', text: 'Never log the API token.' },
        {
          id: 'small-prs',
          severity: 'should',
          text: 'Keep PRs small.',
          scope: { profiles: ['coder', 'perf'], backends: ['claude'] },
        },
      ],
    },
    {
      name: 'local',
      path: '/repo/.itervox/rules.local.json',
      writable: true,
      exists: false,
      raw: '',
      version: '',
      rules: [],
    },
  ],
};

const RULES_PREVIEW = {
  profile: 'coder',
  backend: 'claude',
  projects: ['repo'],
  rules: [
    { id: 'pnpm', severity: 'must', text: 'Use pnpm.', layer: 'project' },
    { id: 'no-token-logs', severity: 'must', text: 'Never log the API token.', layer: 'project' },
    { id: 'small-prs', severity: 'should', text: 'Keep PRs small.', layer: 'project' },
  ],
  conflicts: [],
  errors: [],
  block: '## Rules\n- MUST: Use pnpm.\n- MUST: Never log the API token.\n- SHOULD: Keep PRs small.',
  hash: 'a3f9c1',
  ids: ['pnpm', 'no-token-logs', 'small-prs'],
  templatePlacesRules: false,
};

function memoryEntry(over: Record<string, unknown>) {
  return {
    id: 'm-1',
    type: 'fact',
    text: 'This repo uses pnpm.',
    scope: 'project',
    status: 'active',
    created: T,
    updated: T,
    path: '/repo/.itervox/memory.json',
    runs: [],
    ...over,
  };
}

const MEMORY = {
  enabled: true,
  tokenBudget: 2000,
  promptShareTokens: 8000,
  schemaUrl: 'https://itervox.dev/schema/memory.schema.json',
  files: [
    { scope: 'private', path: '/home/me/.itervox/memory.json', exists: true, writable: true },
    { scope: 'project', path: '/repo/.itervox/memory.json', exists: true, writable: true },
  ],
  entries: [
    memoryEntry({ tags: ['tooling'], useCount: 4, lastUsed: T }),
    memoryEntry({
      id: 'm-2',
      type: 'decision',
      text: 'Staging is read-only on Fridays.',
      profiles: ['reviewer'],
    }),
    memoryEntry({
      id: 'm-3',
      type: 'lesson',
      status: 'proposed',
      text: 'When a feature needs CSV, reuse the csv helper instead of adding a dependency.',
      source: { kind: 'lesson', issue: 'DEMO-1' },
    }),
  ],
};

const TOKENS = {
  tokens: [
    {
      id: 't-1',
      name: 'teammate laptop',
      scope: 'operator',
      created: T,
      expires: '2027-01-01T00:00:00Z',
    },
    { id: 't-2', name: 'phone', scope: 'viewer', created: T },
  ],
};

function skill(name: string, tokens: number) {
  return { Name: name, Provider: 'claude', Source: 'project', ApproxTokens: tokens };
}

const INVENTORY = {
  ScanTime: T,
  Stale: false,
  Skills: ['deploy', 'review', 'triage', 'release'].map((n) => skill(n, 900)),
  Subagents: [
    { Name: 'planner', Provider: 'claude', Source: 'project', ApproxTokens: 120 },
    { Name: 'tester', Provider: 'claude', Source: 'project', ApproxTokens: 90 },
  ],
  Plugins: [],
  MCPServers: [{ Name: 'github', Source: 'project', Transport: 'stdio' }],
  Hooks: [
    {
      Event: 'PreToolUse',
      Command: 'make lint',
      Provider: 'claude',
      Source: 'project',
      ApproxTokens: 6,
    },
  ],
  Instructions: [
    {
      Name: 'CLAUDE.md',
      Provider: 'claude',
      Scope: 'project',
      FilePath: 'CLAUDE.md',
      ApproxTokens: 2400,
    },
    {
      Name: 'AGENTS.md',
      Provider: 'codex',
      Scope: 'project',
      FilePath: 'AGENTS.md',
      ApproxTokens: 300,
    },
  ],
  Issues: [],
};

const INVENTORY_ISSUES = [
  {
    ID: 'duplicate_skill_name',
    Severity: 'error',
    Title: 'Duplicate skill name',
    Description: 'Two skills share the name "deploy"; the agent loads only one of them.',
  },
  {
    ID: 'unused_mcp_server',
    Severity: 'warn',
    Title: 'Unused MCP server',
    Description: 'An MCP server adds tokens to every run but no profile uses its tools.',
  },
];

function hubProject(name: string, over: Record<string, unknown>) {
  return {
    name,
    workflow: `/code/${name}/WORKFLOW.md`,
    projectDir: `/code/${name}`,
    pid: 1,
    url: 'http://127.0.0.1:8090/',
    version: 'v0.3.1',
    startedAt: T,
    health: 'ok',
    proxied: true,
    capabilities: ['dispatch_pause'],
    ...over,
  };
}

const HUB_PROJECTS = {
  hub: { version: 'v0.3.1', pid: 42, url: 'http://127.0.0.1:8089/hub/', startedAt: T, headSeq: 7 },
  projects: [
    hubProject('web-app', { summary: { counts: { running: 3, inputRequired: 1, retrying: 1 } } }),
    hubProject('api-server', { summary: { counts: { running: 1, inputRequired: 1 } } }),
    hubProject('docs-site', { summary: { counts: { running: 0 } } }),
  ],
};

// The published settings schema (the daemon serves the same document), so
// the All settings section renders every real field.
const SETTINGS_SCHEMA: unknown = JSON.parse(
  readFileSync(
    resolve(
      dirname(fileURLToPath(import.meta.url)),
      '../../../site/public/schema/settings.schema.json',
    ),
    'utf8',
  ),
);

function settingsLayer(name: string, path: string, values: Record<string, unknown>) {
  return {
    name,
    path,
    writable: true,
    exists: true,
    raw: '{}',
    version: 'v1',
    values,
    unknown: [],
  };
}

const SETTINGS_LAYERS = {
  schemaUrl: 'https://itervox.dev/schema/settings.schema.json',
  layers: [
    settingsLayer('user', '/home/me/.itervox/settings.jsonc', {}),
    settingsLayer('project', '/repo/.itervox/settings.jsonc', {
      'agent.isolation': 'process',
      'agent.max_concurrent_agents': 3,
    }),
    settingsLayer('local', '/repo/.itervox/settings.local.jsonc', {}),
  ],
  effective: [
    { key: 'agent.isolation', value: 'process', layer: 'project' },
    { key: 'agent.max_concurrent_agents', value: 3, layer: 'project' },
  ],
};

const HUB_NOTIFICATIONS = {
  settings: {
    version: 1,
    digest_interval_minutes: 30,
    channels: { push: { enabled: true }, slack: { enabled: true }, desktop: { enabled: true } },
    defaults: {
      input_required: ['push', 'slack', 'desktop'],
      guardrail_approval: ['push', 'desktop'],
      run_failed: ['slack', 'desktop'],
      pr_merged: ['slack'],
    },
    projects: {},
  },
  kinds: [
    { name: 'input_required', urgent: true },
    { name: 'guardrail_approval', urgent: true },
    { name: 'run_failed', urgent: false },
    { name: 'pr_merged', urgent: false },
  ],
  channels: [
    { name: 'push', available: true },
    { name: 'slack', available: true },
    { name: 'desktop', available: true },
  ],
  projects: ['web-app'],
};

// A run waiting for an answer, for the phone's issue page.
const RUN_CONTEXT = {
  identifier: 'DEMO-1',
  run: {
    runId: 'run-1',
    live: true,
    profile: 'coder',
    backend: 'codex',
    currentStep: 'Paused to ask before changing how totals are rounded.',
    turnCount: 4,
    totalTokens: 18200,
    pendingInput: 'Round each line item, or only the invoice total?',
    steers: [],
  },
  changedFiles: [{ path: 'src/invoice.ts', status: 'M', additions: 12, deletions: 3 }],
  handoffs: [],
};

/** Fill the pages the default mock leaves empty. */
export function installTourData(api: MockApiHandle): void {
  const fixed = (re: RegExp, body: unknown) => {
    api.override(re, (route) =>
      route.request().method() === 'GET' ? route.fulfill(json(body)) : route.fulfill(json({})),
    );
  };
  fixed(/\/api\/v1\/rules\/preview/, RULES_PREVIEW);
  fixed(/\/api\/v1\/rules$/, RULES);
  fixed(/\/api\/v1\/memory$/, MEMORY);
  fixed(/\/api\/v1\/issues\/DEMO-1\/context$/, RUN_CONTEXT);
  fixed(/\/api\/v1\/tokens$/, TOKENS);
  fixed(/\/api\/v1\/settings\/schema$/, SETTINGS_SCHEMA);
  fixed(/\/api\/v1\/settings\/layers$/, SETTINGS_LAYERS);
  fixed(/\/api\/v1\/skills\/inventory$/, INVENTORY);
  fixed(/\/api\/v1\/skills\/issues$/, INVENTORY_ISSUES);
  fixed(/\/hub\/api\/v1\/projects$/, HUB_PROJECTS);
  fixed(/\/hub\/api\/v1\/whoami$/, { scope: 'admin' });
  fixed(/\/hub\/api\/v1\/notifications$/, HUB_NOTIFICATIONS);
}

// Run context for a live run (the issue detail's Now card, steering, Details /
// Changes tabs, decisions and the playbook select), keyed by identifier.
function liveRunContext(identifier: string) {
  return {
    identifier,
    run: {
      runId: `run-${identifier}`,
      live: true,
      profile: 'default',
      backend: 'claude',
      startedAt: T,
      updatedAt: T,
      currentStep: 'Updating the greeting module, then running the unit tests.',
      lastToolCall: { name: 'Bash', target: 'go test ./internal/greeting/...', at: T },
      toolCalls: 12,
      turnCount: 3,
      totalTokens: 4200,
      queuedSteers: 1,
      steers: [{ text: 'Keep the old greeting as a fallback.', at: T, delivery: 'delivered' }],
      playbook: 'refactor',
    },
    changedFiles: [
      { path: 'internal/greeting/greeting.go', status: 'M', additions: 18, deletions: 4 },
      { path: 'internal/greeting/greeting_test.go', status: 'M', additions: 22, deletions: 0 },
    ],
    handoffs: [],
  };
}

const PLAYBOOKS = {
  enabled: true,
  playbooks: [
    {
      name: 'refactor',
      description: 'Behaviour-preserving restructuring',
      keywords: ['refactor'],
      requireEvidence: [],
    },
    {
      name: 'bugfix',
      description: 'Reproduce, fix, add a regression test',
      keywords: ['bug'],
      requireEvidence: [],
    },
  ],
};

function decisions(identifier: string) {
  return {
    identifier,
    runs: [
      {
        path: `.itervox/handoff/2026-10-01T12-00-00Z_coder.md`,
        runTimestamp: T,
        role: 'coder',
        decisions: [
          {
            decision: 'Keep the old greeting as a fallback',
            why: 'callers outside this repo still use it',
            alternative: 'remove it now',
          },
        ],
      },
    ],
  };
}

/** The endpoints behind the run_context capability, for the issue detail. */
export function installRunContextData(api: MockApiHandle): void {
  const idOf = (url: string, tail: string) =>
    decodeURIComponent(new URL(url).pathname.split('/issues/')[1]?.replace(tail, '') ?? '');
  api.override(/\/api\/v1\/issues\/[^/]+\/context$/, (route) =>
    route.fulfill(json(liveRunContext(idOf(route.request().url(), '/context')))),
  );
  api.override(/\/api\/v1\/issues\/[^/]+\/decisions$/, (route) =>
    route.fulfill(json(decisions(idOf(route.request().url(), '/decisions')))),
  );
  api.override(/\/api\/v1\/playbooks$/, (route) => route.fulfill(json(PLAYBOOKS)));
  api.override(/\/api\/v1\/steering\/phrases$/, (route) =>
    route.fulfill(json({ phrases: [{ label: 'Wrap up', message: 'Wrap up and open the PR.' }] })),
  );
}

// Review and continue — the Changes tab's data: a PR diff with two files,
// each file's patch, and the review routes.
const CHANGES_FILES = [
  { path: 'internal/greeting/greeting.go', status: 'modified', additions: 4, deletions: 2 },
  { path: 'internal/greeting/greeting_test.go', status: 'modified', additions: 3, deletions: 0 },
];
const CHANGES_PATCHES: Record<string, string> = {
  'internal/greeting/greeting.go': [
    'diff --git a/internal/greeting/greeting.go b/internal/greeting/greeting.go',
    '--- a/internal/greeting/greeting.go',
    '+++ b/internal/greeting/greeting.go',
    '@@ -8,7 +8,9 @@ package greeting',
    ' // Hello returns the greeting for name.',
    ' func Hello(name string) string {',
    '-\tif name == "" {',
    '-\t\treturn "Hello!"',
    '+\tname = strings.TrimSpace(name)',
    '+\tif name == "" {',
    '+\t\treturn fallbackGreeting',
    ' \t}',
    '+\t// Keep the old greeting as a fallback.',
    ' \treturn "Hello, " + name + "!"',
    ' }',
    '',
  ].join('\n'),
  'internal/greeting/greeting_test.go': [
    'diff --git a/internal/greeting/greeting_test.go b/internal/greeting/greeting_test.go',
    '--- a/internal/greeting/greeting_test.go',
    '+++ b/internal/greeting/greeting_test.go',
    '@@ -4,3 +4,6 @@ import "testing"',
    ' func TestHello(t *testing.T) {',
    ' \tif got := Hello("Ada"); got != "Hello, Ada!" {',
    ' \t\tt.Fatal(got)',
    '+\t}',
    '+\tif got := Hello("  "); got != fallbackGreeting {',
    '+\t\tt.Fatal(got)',
    '',
  ].join('\n'),
};

export function installChangesData(api: MockApiHandle): void {
  installRunContextData(api);
  api.override(/\/api\/v1\/issues\/[^/]+\/diff(\?|$)/, (route) => {
    const url = new URL(route.request().url());
    const identifier = decodeURIComponent(
      url.pathname.split('/issues/')[1]?.replace('/diff', '') ?? '',
    );
    const file = url.searchParams.get('file');
    const base = { identifier, base: 'main', source: 'pr' };
    if (file) {
      const patch = CHANGES_PATCHES[file] ?? '';
      return route.fulfill(
        json({
          ...base,
          file: CHANGES_FILES.find((f) => f.path === file),
          patch,
          bytes: patch.length,
          maxBytes: 262144,
        }),
      );
    }
    return route.fulfill(
      json({
        ...base,
        mergeBase: 'b1c2d3e4f5a6',
        headSha: 'a9b8c7d6e5f4a3b2',
        hunksAllowed: true,
        sources: ['pr', 'worktree'],
        prUrl: 'https://github.com/example/greeting/pull/12',
        worktreePath: `/work/${identifier}`,
        files: CHANGES_FILES,
      }),
    );
  });
  api.override(/\/api\/v1\/issues\/[^/]+\/review-continue$/, (route) =>
    route.fulfill({
      status: 202,
      contentType: 'application/json',
      body: JSON.stringify({
        agent: { delivery: 'queued', queuedAt: T, movedTo: 'In Progress' },
        pr: { posted: true, prUrl: 'https://github.com/example/greeting/pull/12', comments: 2 },
      }),
    }),
  );
}

/**
 * The issue's changes (GET /issues/{id}/diff and one file's patch), for the
 * issue view's Changes column: the files the live run context lists.
 */
export function installDiffData(api: MockApiHandle): void {
  api.override(/\/api\/v1\/issues\/[^/]+\/diff(\?|$)/, (route) => {
    const url = new URL(route.request().url());
    const identifier = decodeURIComponent(
      url.pathname.split('/issues/')[1]?.replace('/diff', '') ?? '',
    );
    const files = liveRunContext(identifier).changedFiles;
    const file = url.searchParams.get('file');
    if (file === null) {
      return route.fulfill(
        json({
          identifier,
          base: 'main',
          mergeBase: '0f3c2a9d1e7b4c55aa21',
          files,
          hunksAllowed: true,
          source: 'worktree',
          sources: ['worktree'],
        }),
      );
    }
    const f = files.find((x) => x.path === file) ?? files[0];
    return route.fulfill(
      json({
        identifier,
        base: 'main',
        file: f,
        bytes: 240,
        maxBytes: 1_000_000,
        source: 'worktree',
        patch: [
          `diff --git a/${f.path} b/${f.path}`,
          `--- a/${f.path}`,
          `+++ b/${f.path}`,
          '@@ -1,4 +1,5 @@',
          ' func Greeting(name string) string {',
          '-\treturn "Hello, " + name',
          '+\tif name == "" {',
          '+\t\treturn legacyGreeting()',
          '+\t}',
          '+\treturn fmt.Sprintf("Hello, %s!", name)',
          ' }',
          '',
        ].join('\n'),
      }),
    );
  });
}

// System visibility — a live run's resource sample, its activity audit and
// the snapshot's system block (the workspace disk below the warning
// threshold), for the issue view, the issue detail and the dashboard.
const GIB = 1024 ** 3;

export const SYSTEM_USAGE = {
  runId: 'run-DEMO-RUN-1',
  identifier: 'DEMO-RUN-1',
  live: true,
  cpuPercent: 82.5,
  cpuSeconds: 41.2,
  rssBytes: 734_003_200,
  peakRssBytes: 912_261_120,
  processes: 6,
  peakProcesses: 9,
  diskReadBytes: 12_582_912,
  diskWriteBytes: 3_145_728,
  diskIoKnown: true,
  netConnections: 2,
  topCommands: [
    {
      pid: 4242,
      name: 'node',
      args: 'cli.js --print --output-format stream-json',
      cpuPercent: 61.2,
      rssBytes: 536_870_912,
    },
    {
      pid: 4301,
      name: 'go',
      args: 'test ./internal/greeting/...',
      cpuPercent: 18.4,
      rssBytes: 150_994_944,
    },
    { pid: 4300, name: 'git', args: 'status --short', cpuPercent: 0.4, rssBytes: 8_388_608 },
  ],
  samples: 30,
  startedAt: T,
  sampledAt: T,
};

export const SYSTEM_STATUS = (diskFreeGB: number, critical = false) => ({
  platform: 'linux',
  activityMode: 'sampled',
  runs: { runs: 1, cpuPercent: 82.5, rssBytes: 734_003_200, processes: 6 },
  machine: {
    load1: 2.1,
    load5: 1.7,
    load15: 1.2,
    loadKnown: true,
    cpuCount: 8,
    memTotalBytes: 16 * GIB,
    memAvailableBytes: 6 * GIB,
    memPressure: 'ok',
    disk: {
      path: '/home/dev/.itervox/workspaces/web-app',
      freeBytes: diskFreeGB * GIB,
      totalBytes: 500 * GIB,
    },
    diskWarnBytes: 5 * GIB,
    diskPauseBytes: critical ? 2 * GIB : 0,
    diskLow: diskFreeGB < 5,
    diskCritical: critical,
    sampledAt: T,
  },
});

const at = { firstAt: T, lastAt: T };
export const SYSTEM_ACTIVITY = (identifier: string) => ({
  identifier,
  runId: `run-${identifier}`,
  live: true,
  mode: 'sampled',
  platform: 'linux',
  supports: { commands: true, files: true, connections: true },
  notes: [
    'Sampled every 2s from /proc: short-lived processes, files and connections between two samples may be missed.',
  ],
  workspace: `/work/${identifier}`,
  startedAt: T,
  updatedAt: T,
  commands: [
    {
      name: 'node',
      args: 'cli.js --print --output-format stream-json',
      pid: 4242,
      count: 1,
      ...at,
    },
    { name: 'git', args: 'status --short', pid: 4300, count: 4, ...at },
    { name: 'go', args: 'test ./internal/greeting/...', pid: 4301, count: 2, ...at },
    { name: 'gofmt', args: '-l internal/greeting', pid: 4310, count: 1, ...at },
  ],
  files: [
    {
      path: `/work/${identifier}/internal/greeting/greeting.go`,
      write: true,
      command: 'node',
      count: 3,
      ...at,
    },
    {
      path: `/work/${identifier}/internal/greeting/greeting_test.go`,
      write: true,
      command: 'node',
      count: 2,
      ...at,
    },
    { path: `/work/${identifier}/go.mod`, write: false, command: 'go', count: 2, ...at },
    {
      path: '/home/dev/.cache/go-build/trim.txt',
      write: true,
      outsideWorkspace: true,
      command: 'go',
      count: 1,
      ...at,
    },
  ],
  connections: [
    {
      protocol: 'tcp',
      remote: '160.79.104.10:443',
      state: 'ESTABLISHED',
      command: 'node',
      count: 6,
      ...at,
    },
    {
      protocol: 'tcp',
      remote: '140.82.112.3:443',
      state: 'TIME_WAIT',
      command: 'git',
      count: 1,
      ...at,
    },
  ],
  dropped: { commands: 0, files: 0, connections: 0 },
  runs: [{ runId: `run-${identifier}`, startedAt: T, live: true }],
});

/**
 * The run context with its resources, the activity route, and the diff:
 * the issue view and the issue detail with system visibility on. The
 * context override is registered first so it answers before the plain one.
 */
export function installSystemVisibilityData(api: MockApiHandle): void {
  const idOf = (url: string, tail: string) =>
    decodeURIComponent(new URL(url).pathname.split('/issues/')[1]?.replace(tail, '') ?? '');
  api.override(/\/api\/v1\/issues\/[^/]+\/context$/, (route) => {
    const ctx = liveRunContext(idOf(route.request().url(), '/context'));
    return route.fulfill(json({ ...ctx, run: { ...ctx.run, resources: SYSTEM_USAGE } }));
  });
  api.override(/\/api\/v1\/issues\/[^/]+\/activity(\?|$)/, (route) =>
    route.fulfill(
      json(SYSTEM_ACTIVITY(idOf(route.request().url().split('?')[0] ?? '', '/activity'))),
    ),
  );
  installRunContextData(api);
  installDiffData(api);
}
