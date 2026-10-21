// Repo Analysis queries (#200): prompt versions (#186) with their per-digest
// stats, the runs of one version, the repository readiness checks, and
// (#201) the persisted eval results and the failure hotspots.
// Read-only; every request goes through apiRequest → authedFetch.

import { useQuery } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import {
  HistoryResponseSchema,
  PromptVersionsResponseSchema,
  RepoEvalsResponseSchema,
  RepoHotspotsResponseSchema,
  RepoReadinessResponseSchema,
  type HistoryRow,
  type PromptVersionRow,
  type RepoEvalsResponse,
  type RepoHotspotsResponse,
  type RepoReadinessCheck,
} from '../types/schemas';

export const PROMPT_VERSIONS_KEY = ['repo', 'promptVersions'] as const;
export const REPO_READINESS_KEY = ['repo', 'readiness'] as const;
export const REPO_EVALS_KEY = ['repo', 'evals'] as const;
export const REPO_HOTSPOTS_KEY = ['repo', 'hotspots'] as const;

const PROMPT_VERSIONS_STALE_MS = 30_000;
// Readiness probes the tracker; do not re-run it on every focus.
const READINESS_STALE_MS = 5 * 60 * 1000;
// Eval reports change only when someone runs `make evals`.
const EVALS_STALE_MS = 60_000;
// The server refreshes hotspots at most every 10 minutes; while its
// background job runs, poll until the result lands.
const HOTSPOTS_STALE_MS = 60_000;
export const HOTSPOTS_REFRESHING_POLL_MS = 3_000;

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

/** The latest persisted eval result per case (#201). Never starts a run. */
export function useRepoEvals() {
  return useQuery({
    queryKey: REPO_EVALS_KEY,
    queryFn: async (): Promise<RepoEvalsResponse> => {
      const { data } = await apiRequest('/api/v1/repo/evals', { op: 'eval results fetch' });
      return RepoEvalsResponseSchema.parse(data);
    },
    staleTime: EVALS_STALE_MS,
  });
}

/** The failure hotspots (#201); polls while the server's job is refreshing. */
export function useRepoHotspots() {
  return useQuery({
    queryKey: REPO_HOTSPOTS_KEY,
    queryFn: async (): Promise<RepoHotspotsResponse> => {
      const { data } = await apiRequest('/api/v1/repo/hotspots', { op: 'hotspots fetch' });
      return RepoHotspotsResponseSchema.parse(data);
    },
    staleTime: HOTSPOTS_STALE_MS,
    refetchInterval: (query) =>
      query.state.data?.refreshing ? HOTSPOTS_REFRESHING_POLL_MS : false,
  });
}
