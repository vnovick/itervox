import { defineConfig, devices } from '@playwright/test';

/**
 * Guide screenshots (docs). Not a test lane: `make guide-shots` (or
 * `pnpm guide-shots`) runs the specs under `e2e/guide-shots/` against Vite's
 * dev server with every `/api/v1/*` call route-mocked (the Lane-2 fixtures),
 * and writes each shot in dark and light to
 * `site/public/screenshots/guides/<guide>/<nn>-<slug>-<theme>.webp`.
 * No other config matches `guide-shots/`, so CI never runs these.
 *
 * PW_CHROMIUM_PATH points Playwright at a Chromium other than the one its
 * pinned version downloads (e.g. a preinstalled browser).
 */
const executablePath = process.env.PW_CHROMIUM_PATH || undefined;

export default defineConfig({
  testDir: './e2e/guide-shots',
  testMatch: '*.spec.ts',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: true,
  retries: 0,
  reporter: 'list',
  webServer: {
    command: 'pnpm dev --port 5174 --strictPort',
    port: 5174,
    reuseExistingServer: true,
    timeout: 60_000,
  },
  use: {
    baseURL: 'http://localhost:5174',
    actionTimeout: 10_000,
    trace: 'retain-on-failure',
    launchOptions: executablePath ? { executablePath } : {},
  },
  projects: [
    {
      name: 'guides',
      use: {
        ...devices['Desktop Chrome'],
        viewport: { width: 1440, height: 900 },
        deviceScaleFactor: 2,
      },
    },
  ],
});
