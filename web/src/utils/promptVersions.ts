import type { HistoryRow } from '../types/schemas';

// #186 — prompt versions of run history. A run's promptDigest is the
// SHA-256 of its SOUL.md + INSTRUCTIONS.md + rules + template; the dashboard
// treats it as opaque and shows its first PROMPT_DIGEST_SHORT_LEN chars.

export const PROMPT_DIGEST_SHORT_LEN = 12;

export function shortDigest(digest: string): string {
  return digest.slice(0, PROMPT_DIGEST_SHORT_LEN);
}

export interface PromptVersionSummary {
  digest: string;
  runs: number;
  succeeded: number;
  /** succeeded / runs, 0–1. */
  successRate: number;
  lastStartedAt: string;
}

/**
 * Groups history rows by prompt digest (rows without one are skipped), most
 * recently used version first — the client-side twin of
 * GET /api/v1/history/prompt-versions.
 */
export function groupByPromptVersion(rows: readonly HistoryRow[]): PromptVersionSummary[] {
  const byDigest = new Map<string, PromptVersionSummary>();
  for (const row of rows) {
    const digest = row.promptDigest;
    if (!digest) continue;
    const v = byDigest.get(digest) ?? {
      digest,
      runs: 0,
      succeeded: 0,
      successRate: 0,
      lastStartedAt: row.startedAt,
    };
    v.runs += 1;
    if (row.status === 'succeeded') v.succeeded += 1;
    if (row.startedAt > v.lastStartedAt) v.lastStartedAt = row.startedAt;
    byDigest.set(digest, v);
  }
  return [...byDigest.values()]
    .map((v) => ({ ...v, successRate: v.succeeded / v.runs }))
    .sort((a, b) =>
      a.lastStartedAt === b.lastStartedAt
        ? a.digest.localeCompare(b.digest)
        : b.lastStartedAt.localeCompare(a.lastStartedAt),
    );
}
