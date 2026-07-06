// Deterministic time helpers for test fixtures. Both Vitest and Playwright
// must derive timestamps from these so a scenario rendered in a unit test has
// byte-identical timestamps to the same scenario in a browser test.

export const BASE_TIME = new Date('2026-04-29T00:00:00Z');

export function secondsAgo(n: number): Date {
  return new Date(BASE_TIME.getTime() - n * 1000);
}

export function minutesAgo(n: number): Date {
  return secondsAgo(n * 60);
}

export function hoursAgo(n: number): Date {
  return minutesAgo(n * 60);
}

export function formatRFC3339(d: Date): string {
  return d.toISOString();
}
