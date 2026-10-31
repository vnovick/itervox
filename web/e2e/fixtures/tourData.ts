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
