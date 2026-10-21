// Repo Analysis queries (#200): prompt versions (#186) with their per-digest
// stats, the runs of one version, and the repository readiness checks.
// Read-only; every request goes through apiRequest → authedFetch.

import { useQuery } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import {
  HistoryResponseSchema,
  PromptVersionsResponseSchema,
  RepoReadinessResponseSchema,
  type HistoryRow,
  type PromptVersionRow,
  type RepoReadinessCheck,
} from '../types/schemas';

export const PROMPT_VERSIONS_KEY = ['repo', 'promptVersions'] as const;
export const REPO_READINESS_KEY = ['repo', 'readiness'] as const;

const PROMPT_VERSIONS_STALE_MS = 30_000;
// Readiness probes the tracker; do not re-run it on every focus.
const READINESS_STALE_MS = 5 * 60 * 1000;

export function usePromptVersions() {
  return useQuery({
    queryKey: PROMPT_VERSIONS_KEY,
    queryFn: async (): Promise<{ versions: PromptVersionRow[]; unversionedRuns: number }> => {
      const { data } = await apiRequest('/api/v1/history/prompt-versions', {
        op: 'prompt versions fetch',
      });
      const parsed = PromptVersionsResponseSchema.parse(data);
      return { versions: parsed.versions ?? [], unversionedRuns: parsed.unversionedRuns ?? 0 };
    },
    staleTime: PROMPT_VERSIONS_STALE_MS,
  });
}

/** The runs of one prompt version (GET /api/v1/history?prompt=). Idle for null. */
export function usePromptVersionRuns(digest: string | null) {
  return useQuery({
    queryKey: ['repo', 'promptVersionRuns', digest] as const,
    enabled: digest !== null,
    queryFn: async (): Promise<HistoryRow[]> => {
      const { data } = await apiRequest(
        `/api/v1/history?prompt=${encodeURIComponent(digest ?? '')}`,
        { op: 'prompt version runs fetch' },
      );
      return HistoryResponseSchema.parse(data).history ?? [];
    },
    staleTime: PROMPT_VERSIONS_STALE_MS,
  });
}

export function useRepoReadiness() {
  return useQuery({
    queryKey: REPO_READINESS_KEY,
    queryFn: async (): Promise<{ checks: RepoReadinessCheck[]; generatedAt?: string }> => {
      const { data } = await apiRequest('/api/v1/repo/readiness', { op: 'readiness fetch' });
      const parsed = RepoReadinessResponseSchema.parse(data);
      return { checks: parsed.checks ?? [], generatedAt: parsed.generatedAt };
    },
    staleTime: READINESS_STALE_MS,
  });
}
