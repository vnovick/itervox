// Reusable Playwright assertions for Lane-2 specs.

import { expect, type Page } from '@playwright/test';
import type { RecordedMutation } from './mockApi';

export interface MutationMatcher {
  method: string;
  /** Tested against the FULL request URL. */
  urlMatcher: RegExp;
  /** Optional payload predicate. */
  bodyMatcher?: (body: unknown) => boolean;
}

export function expectMutation(
  recorded: RecordedMutation[],
  matcher: MutationMatcher,
): RecordedMutation {
  const found = recorded.find(
    (m) =>
      m.method === matcher.method &&
      matcher.urlMatcher.test(m.url) &&
      (!matcher.bodyMatcher || matcher.bodyMatcher(m.body)),
  );
  expect(
    found,
    `expected ${matcher.method} ${matcher.urlMatcher} mutation, got: ${JSON.stringify(recorded)}`,
  ).toBeTruthy();
  return found!;
}

/**
 * Listen for console errors and pageerrors after this call. Returns a getter
 * that returns the accumulated errors. Use in afterEach with expect(...).toEqual([]).
 */
export function watchConsoleErrors(page: Page): () => string[] {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`));
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(`console.error: ${msg.text()}`);
  });
  return () => errors.slice();
}

export function expectNoConsoleErrors(getErrors: () => string[]): void {
  const errs = getErrors();
  expect(errs, `expected no console/page errors, got:\n${errs.join('\n')}`).toEqual([]);
}
