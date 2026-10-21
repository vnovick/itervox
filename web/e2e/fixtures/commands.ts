// High-level boot helper. Encapsulates the URL-token capture path so each
// spec doesn't repeat the route+navigate dance.

import type { Page } from '@playwright/test';
import { installMockApi, type MockApiHandle } from './mockApi';
import { installMockSse, type SseMode } from './mockSse';
import type { Scenario } from './scenarios';
import { E2E_TOKEN } from './scenarios';

export interface BootOptions {
  scenario: Scenario;
  /** Default: E2E_TOKEN. Pass null to boot without a token (token-entry path). */
  token?: string | null;
  /** Default: '/'. */
  route?: string;
  /** Default: 'one-shot'. */
  sseMode?: SseMode;
}

export interface BootResult {
  api: MockApiHandle;
}

export async function bootApp(page: Page, options: BootOptions): Promise<BootResult> {
  const { scenario, token = E2E_TOKEN, route = '/', sseMode = 'one-shot' } = options;

  const api = await installMockApi(page, scenario);
  await installMockSse(page, scenario, sseMode);

  // The token query goes before any #hash (e.g. /repo#usage, #200).
  const hashAt = route.indexOf('#');
  const path = hashAt < 0 ? route : route.slice(0, hashAt);
  const hash = hashAt < 0 ? '' : route.slice(hashAt);
  const target = token ? `${path}?token=${encodeURIComponent(token)}${hash}` : route;
  await page.goto(target);

  return { api };
}
