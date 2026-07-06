import { defineConfig, devices } from '@playwright/test';

/**
 * Route-mocked Playwright config (T-61).
 *
 * Lane 2 of the QA framework: browser tests that mount the dashboard against
 * Vite's dev server with every `/api/v1/*` endpoint mocked via `page.route`.
 * No daemon binary required; no real Linear/GitHub/Claude/Codex.
 *
 * Coexists with `playwright.config.ts` (Lane 3 — real-daemon e2e). Test
 * matchers don't overlap: this config only picks up `ui-current-*.spec.ts`.
 */
export default defineConfig({
  testDir: './e2e',
  testMatch: 'ui-current-*.spec.ts',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  // Route-mocked specs are independent — no shared port/daemon — so they
  // can run fully parallel.
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  webServer: {
    command: 'pnpm dev --port 5173',
    port: 5173,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
  use: {
    baseURL: 'http://localhost:5173',
    actionTimeout: 5_000,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium-desktop',
      // Mobile-shell smoke runs only on chromium-mobile — exclude it here so
      // the runner doesn't surface 6 "skipped" rows under the desktop project.
      testIgnore: 'ui-current-mobile-smoke.spec.ts',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1440, height: 900 } },
    },
    {
      name: 'chromium-mobile',
      // iPhone 14 viewport — current shell must remain usable at this width.
      // Only the explicitly mobile-shaped smoke spec runs on this project.
      testMatch: 'ui-current-mobile-smoke.spec.ts',
      use: { ...devices['Desktop Chrome'], viewport: { width: 390, height: 844 } },
    },
  ],
});
