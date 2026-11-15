// #215 — the run context and the worktree diff of one issue. Read-only;
// every request goes through apiRequest → authedFetch.

import { useQuery } from '@tanstack/react-query';
import { ApiError, apiRequest } from '../auth/apiRequest';
import {
  DiffResponseSchema,
  FileDiffResponseSchema,
  RunContextResponseSchema,
  type DiffResponse,
  type FileDiffResponse,
  type RunContextResponse,
} from '../types/schemas';

/** The daemon capability that advertises both endpoints. */
export const RUN_CONTEXT_CAPABILITY = 'run_context';

/** How often the Now panel re-reads a live run's context. */
export const RUN_CONTEXT_POLL_MS = 3_000;
const DIFF_STALE_MS = 10_000;

const enc = encodeURIComponent;

/** GET path, answering null on a 404 (no run / no worktree). */
async function getOrNull(path: string, op: string): Promise<unknown> {
  try {
    const { data } = await apiRequest(path, { op });
    return data;
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

export const RUN_CONTEXT_KEY = (identifier: string) => ['runContext', identifier] as const;
/** A diff source the daemon serves (#238); undefined = the daemon's default. */
export type DiffSource = 'pr' | 'worktree';

export const ISSUE_DIFF_KEY = (identifier: string, source?: DiffSource) =>
  ['issueDiff', identifier, source ?? 'default'] as const;
export const FILE_DIFF_KEY = (identifier: string, file: string, source?: DiffSource) =>
  ['issueDiff', identifier, source ?? 'default', 'file', file] as const;

/**
 * The run context; `live` polls every RUN_CONTEXT_POLL_MS while the run is
 * going. Resolves to null when the issue has no run and no worktree (404).
 */
export function useRunContext(identifier: string, opts: { enabled: boolean; live: boolean }) {
  return useQuery({
    queryKey: RUN_CONTEXT_KEY(identifier),
    enabled: opts.enabled && identifier !== '',
    queryFn: async (): Promise<RunContextResponse | null> => {
      const data = await getOrNull(
        `/api/v1/issues/${enc(identifier)}/context`,
        'run context fetch',
      );
      return data === null ? null : RunContextResponseSchema.parse(data);
    },
    refetchInterval: opts.live ? RUN_CONTEXT_POLL_MS : false,
    staleTime: opts.live ? 0 : DIFF_STALE_MS,
  });
}

/**
 * The changed files: the worktree against the merge base, or — once the
 * issue has a PR (#238) — the PR's files. `source` pins one; undefined lets
 * the daemon choose. null when there is no worktree.
 */
export function useIssueDiff(identifier: string, enabled: boolean, source?: DiffSource) {
  return useQuery({
    queryKey: ISSUE_DIFF_KEY(identifier, source),
    enabled: enabled && identifier !== '',
    queryFn: async (): Promise<DiffResponse | null> => {
      const q = source ? `?source=${source}` : '';
      const data = await getOrNull(`/api/v1/issues/${enc(identifier)}/diff${q}`, 'diff fetch');
      return data === null ? null : DiffResponseSchema.parse(data);
    },
    staleTime: DIFF_STALE_MS,
  });
}

/** One file's unified diff (needs the operator scope). Idle for a null file. */
export function useFileDiff(identifier: string, file: string | null, source?: DiffSource) {
  return useQuery({
    queryKey: FILE_DIFF_KEY(identifier, file ?? '', source),
    enabled: file !== null && identifier !== '',
    queryFn: async (): Promise<FileDiffResponse> => {
      const q = source ? `&source=${source}` : '';
      const { data } = await apiRequest(
        `/api/v1/issues/${enc(identifier)}/diff?file=${enc(file ?? '')}${q}`,
        { op: 'file diff fetch' },
      );
      return FileDiffResponseSchema.parse(data);
    },
    staleTime: DIFF_STALE_MS,
  });
}
