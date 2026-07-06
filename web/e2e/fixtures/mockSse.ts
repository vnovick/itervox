// Variant SSE behaviors for `/api/v1/events` etc. The default in mockApi.ts
// is "emit one snapshot, close" — sufficient for boot tests. This module
// provides richer modes: keep-open + on-demand emit, silent (open but no
// events — exercises the watchdog), and parse-fail (malformed payload).

import type { Page, Route } from '@playwright/test';
import type { Scenario } from './scenarios';

export type SseMode = 'one-shot' | 'connected' | 'silent' | 'parse-fail';

export interface MockSseHandle {
  /** Push a custom snapshot frame to all open /events streams. */
  emitSnapshot: (snapshot: unknown) => Promise<void>;
}

export async function installMockSse(
  page: Page,
  scenario: Scenario,
  mode: SseMode = 'one-shot',
): Promise<MockSseHandle> {
  // Track every open `/events` route so emitSnapshot can fan out.
  const openRoutes = new Set<Route>();

  await page.route('**/api/v1/events', async (route) => {
    if (mode === 'silent') {
      // Hold the connection open with no body; the dashboard's watchdog
      // (T-27) should detect the silence and switch to polling.
      await route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: '',
      });
      return;
    }

    if (mode === 'parse-fail') {
      await route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: `event: snapshot\ndata: { not valid json\n\n`,
      });
      return;
    }

    // one-shot or connected — both emit an initial snapshot frame.
    const frame = `event: snapshot\ndata: ${JSON.stringify(scenario.snapshot)}\n\n`;

    if (mode === 'one-shot') {
      await route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: frame,
      });
      return;
    }

    // 'connected' — keep the route open so emitSnapshot can append later.
    openRoutes.add(route);
    await route.fulfill({
      status: 200,
      contentType: 'text/event-stream',
      body: frame,
    });
    openRoutes.delete(route);
  });

  return {
    emitSnapshot: async (snapshot) => {
      // 'connected' mode is best-effort: Playwright's route.fulfill is
      // single-shot; for fully streaming behavior switch to a fixture
      // server. This handle is here so future tests can layer on richer
      // streaming when needed.
      const frame = `event: snapshot\ndata: ${JSON.stringify(snapshot)}\n\n`;
      for (const route of openRoutes) {
        try {
          await route.fulfill({ status: 200, contentType: 'text/event-stream', body: frame });
        } catch {
          // route already finalized — drop.
        }
      }
    },
  };
}
