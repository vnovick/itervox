// Small builders shared by the guide-shot fixtures. A guide's own data lives
// in fixtures/<guide>.ts; add a helper here only when two guides need it.

import type { Route } from '@playwright/test';
import type { MockApiHandle } from '../../fixtures/mockApi';

/** A fixed timestamp, so captures do not change from run to run. */
export const T = '2026-11-14T10:00:00Z';

export function json(body: unknown) {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) };
}

/** Answer GETs on `re` with `body` (mutations get `{}`), as an override. */
export function fixed(api: MockApiHandle, re: RegExp, body: unknown): void {
  api.override(re, (route: Route) =>
    route.request().method() === 'GET' ? route.fulfill(json(body)) : route.fulfill(json({})),
  );
}

export interface RunContextOver {
  identifier: string;
  run?: Record<string, unknown> | null;
  changedFiles?: { path: string; status: string; additions: number; deletions: number }[];
}

/** A `/issues/{id}/context` body (the issue's Now panel). */
export function runContext({ identifier, run, changedFiles = [] }: RunContextOver) {
  return {
    identifier,
    run:
      run === null
        ? null
        : {
            runId: `run-${identifier}`,
            live: true,
            profile: 'fixer',
            backend: 'claude',
            startedAt: T,
            updatedAt: T,
            toolCalls: 14,
            turnCount: 3,
            totalTokens: 38_400,
            queuedSteers: 0,
            steers: [],
            guardrailBlocks: 0,
            ...run,
          },
    changedFiles,
    handoffs: [],
  };
}

/** Empty answers for the issue detail's side panels. */
export function quietIssuePanels(api: MockApiHandle): void {
  fixed(api, /\/api\/v1\/issues\/[^/]+\/decisions$/, { identifier: '', runs: [] });
  fixed(api, /\/api\/v1\/playbooks$/, { enabled: false, playbooks: [] });
  fixed(api, /\/api\/v1\/steering\/phrases$/, { phrases: [] });
}
