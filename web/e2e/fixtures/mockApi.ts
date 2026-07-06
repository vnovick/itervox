// Route-mocked /api/v1/* handler for Lane-2 Playwright tests.
//
// Contract:
//  - Read-only GETs return scenario data.
//  - Mutations (POST/PUT/PATCH/DELETE) are recorded into `recordedMutations`
//    and return 200 by default, or whatever the override map returns.
//  - Any /api/v1/* request that isn't matched here fails the test loud-fast,
//    so a stray direct fetch surfaces as a missing-mock error rather than a
//    silent component-state mismatch.

import type { Page, Route } from '@playwright/test';
import type { IssueLogEntry, StateSnapshot, TrackerIssue } from '../../src/types/schemas';
import type { Scenario } from './scenarios';

export interface RecordedMutation {
  method: string;
  url: string;
  body: unknown;
}

export interface MockApiHandle {
  recordedMutations: RecordedMutation[];
  /** Replace the active scenario after install — useful for mid-test transitions. */
  setScenario: (scenario: Scenario) => void;
  /** Override one route's response. Last writer wins. */
  override: (matcher: RegExp, handler: (route: Route) => Promise<void> | void) => void;
}

const API_PREFIX = /\/api\/v1\//;

function jsonRoute(route: Route, body: unknown, status = 200) {
  return route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  });
}

function plainTextRoute(route: Route, body: string, status = 200) {
  return route.fulfill({ status, contentType: 'text/plain', body });
}

export async function installMockApi(page: Page, scenario: Scenario): Promise<MockApiHandle> {
  const handle: MockApiHandle = {
    recordedMutations: [],
    setScenario: () => {
      // Replaced below once `current` is captured in the closure.
    },
    override: () => {
      // Replaced below.
    },
  };

  let current: Scenario = scenario;
  const overrides: Array<{ matcher: RegExp; handler: (route: Route) => Promise<void> | void }> = [];

  handle.setScenario = (next) => {
    current = next;
  };
  handle.override = (matcher, handler) => {
    overrides.push({ matcher, handler });
  };

  await page.route('**/api/v1/**', async (route) => {
    const request = route.request();
    const url = request.url();
    const method = request.method();

    // Override hooks first.
    for (const { matcher, handler } of overrides) {
      if (matcher.test(url)) {
        await handler(route);
        return;
      }
    }

    // Mutations: record and return 200 unless an override said otherwise.
    if (method !== 'GET' && method !== 'HEAD') {
      let body: unknown = null;
      try {
        body = request.postDataJSON();
      } catch {
        body = request.postData();
      }
      handle.recordedMutations.push({ method, url, body });
      await jsonRoute(route, { ok: true });
      return;
    }

    // GET endpoints — pattern-match by path.
    const path = new URL(url).pathname;

    if (/\/api\/v1\/health$/.test(path)) {
      return plainTextRoute(route, 'ok');
    }
    if (/\/api\/v1\/state$/.test(path)) {
      return jsonRoute(route, current.snapshot satisfies StateSnapshot);
    }
    if (/\/api\/v1\/events$/.test(path)) {
      // SSE — emit one snapshot frame and close. Full SSE behavior in mockSse.ts.
      const frame = `event: snapshot\ndata: ${JSON.stringify(current.snapshot)}\n\n`;
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: frame,
      });
    }
    if (/\/api\/v1\/issues$/.test(path)) {
      return jsonRoute(route, current.issues satisfies TrackerIssue[]);
    }
    {
      const m = path.match(/\/api\/v1\/issues\/([^/]+)$/);
      if (m) {
        const id = decodeURIComponent(m[1]);
        const issue = current.issues.find((i) => i.identifier === id);
        if (!issue) return jsonRoute(route, { error: 'not_found' }, 404);
        return jsonRoute(route, issue);
      }
    }
    {
      const m = path.match(/\/api\/v1\/issues\/([^/]+)\/logs$/);
      if (m) {
        const id = decodeURIComponent(m[1]);
        const logs: IssueLogEntry[] = current.logs[id] ?? [];
        return jsonRoute(route, logs);
      }
    }
    {
      const m = path.match(/\/api\/v1\/issues\/([^/]+)\/sublogs$/);
      if (m) {
        return jsonRoute(route, []);
      }
    }
    {
      const m = path.match(/\/api\/v1\/issues\/([^/]+)\/(log-stream|sublog-stream)$/);
      if (m) {
        // SSE stream stub — emit the matching log entries (or empty for sublog) and close.
        const id = decodeURIComponent(m[1]);
        const kind = m[2];
        const entries: IssueLogEntry[] = kind === 'log-stream' ? current.logs[id] ?? [] : [];
        const body = entries
          .map((e) => `event: log\ndata: ${JSON.stringify(e)}\n\n`)
          .join('');
        return route.fulfill({ status: 200, contentType: 'text/event-stream', body });
      }
    }
    if (/\/api\/v1\/logs\/identifiers$/.test(path)) {
      return jsonRoute(route, current.issues.map((i) => i.identifier));
    }
    if (/\/api\/v1\/logs$/.test(path)) {
      return jsonRoute(route, []);
    }
    if (/\/api\/v1\/projects(\/filter)?$/.test(path)) {
      return jsonRoute(route, []);
    }
    if (/\/api\/v1\/settings\/models$/.test(path)) {
      return jsonRoute(route, current.snapshot.availableModels ?? {});
    }
    // Skills inventory endpoints — default to "empty inventory + no issues"
    // so any spec that loads /settings (where SkillsCard mounts) doesn't
    // trigger console.error on a 599. Specs that care about skills override
    // these via api.override(...).
    if (/\/api\/v1\/skills\/inventory$/.test(path)) {
      return jsonRoute(route, {
        ScanTime: new Date().toISOString(),
        Skills: [],
        Plugins: [],
        MCPServers: [],
        Hooks: [],
        Instructions: [],
        Issues: [],
      });
    }
    if (/\/api\/v1\/skills\/issues$/.test(path)) {
      return jsonRoute(route, []);
    }
    if (/\/api\/v1\/skills\/analytics$/.test(path)) {
      // Return 200 with an empty-but-valid snapshot. A 503 here would be
      // accurate to the daemon's "no runtime evidence" branch but it shows
      // up in the browser as a `console.error: Failed to load resource (503)`
      // which trips the `expectNoConsoleErrors` guard in unrelated specs.
      return jsonRoute(route, {
        GeneratedAt: new Date().toISOString(),
        SkillStats: [],
        HookStats: [],
        ProfileCosts: [],
        Recommendations: [],
      });
    }
    if (/\/api\/v1\/skills\/analytics\/recommendations$/.test(path)) {
      return jsonRoute(route, []);
    }

    // Unhandled — fail loud so the spec author sees a missing route stub.
    await route.fulfill({
      status: 599,
      contentType: 'text/plain',
      body: `mockApi: unhandled GET ${path} — add a stub in mockApi.ts`,
    });
  });

  return handle;
}
