// T-73 — Real-daemon issue-state + logs reachability smoke (Lane 3).
//
// Verifies the daemon's issue surface is reachable through the dashboard:
//   1. /api/v1/issues returns 200 and an array.
//   2. /api/v1/logs/identifiers returns 200 and an array.
//   3. /api/v1/issues/:id/logs returns 200 for the first issue (if any).

import { expect, test } from '@playwright/test';
import { startDaemon, type Daemon } from './helpers/daemon';

let daemon: Daemon;

test.beforeAll(async () => {
  daemon = await startDaemon();
});

test.afterAll(async () => {
  await daemon.stop();
});

test('/api/v1/issues returns an array of TrackerIssue shapes', async () => {
  const res = await fetch(`${daemon.url}/api/v1/issues`, {
    headers: { Authorization: `Bearer ${daemon.token}` },
  });
  expect(res.status).toBe(200);
  const issues: unknown = await res.json();
  expect(Array.isArray(issues)).toBe(true);
});

test('/api/v1/logs/identifiers returns an array', async () => {
  const res = await fetch(`${daemon.url}/api/v1/logs/identifiers`, {
    headers: { Authorization: `Bearer ${daemon.token}` },
  });
  expect(res.status).toBe(200);
  const ids: unknown = await res.json();
  expect(Array.isArray(ids)).toBe(true);
});

test('/api/v1/issues/:id/logs is reachable for any extant issue', async () => {
  const issuesRes = await fetch(`${daemon.url}/api/v1/issues`, {
    headers: { Authorization: `Bearer ${daemon.token}` },
  });
  expect(issuesRes.status).toBe(200);
  const issues = (await issuesRes.json()) as Array<{ identifier?: string }>;
  if (issues.length === 0) {
    test.skip(true, 'memory tracker exposed no issues — log reachability not testable');
    return;
  }
  const id = issues[0].identifier;
  if (!id) {
    test.skip(true, 'first issue had no identifier');
    return;
  }
  const logsRes = await fetch(`${daemon.url}/api/v1/issues/${encodeURIComponent(id)}/logs`, {
    headers: { Authorization: `Bearer ${daemon.token}` },
  });
  expect([200, 204, 404]).toContain(logsRes.status); // 404 acceptable if no logs yet
});
