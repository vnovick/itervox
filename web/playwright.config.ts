import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright e2e config (T-31 / F-NEW-E).
 *
 * The tests under `e2e/` exercise the real dashboard against a real `itervox`
 * daemon. Each test boots a fresh daemon via `e2e/helpers/daemon.ts` so the
 * suite is deterministic across runs and CI workers.
 *
 * Prerequisite: `make build` must have run before `pnpm test:e2e`. The Go
 * binary embeds `web/dist` via //go:embed, so the dashboard bundle has to
 * exist on disk before the daemon serves it. The Makefile's `e2e` target
 * (added alongside this config) handles the build dependency.
 */
export default defineConfig({
  testDir: './e2e',
  // Lane-3 (real-daemon) specs only. The Lane-2 route-mocked specs live in
  // `ui-current-*.spec.ts` and run via `playwright.ui.config.ts`, so we
  // ignore them here — they assume a Vite dev server + mockApi which this
  // config doesn't provide.
  testIgnore: 'ui-current-*.spec.ts',
  // Each spec spawns its own daemon — short timeout is sufficient.
  timeout: 30_000,
  expect: {
    timeout: 5_000,
  },
  fullyParallel: false, // daemons claim a port; run serially to avoid contention
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  use: {
    actionTimeout: 5_000,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
