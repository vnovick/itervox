// System visibility — a run's activity audit (commands, files, network
// connections). Read-only and operator-scoped; every request goes through
// apiRequest → authedFetch.

import { useQuery } from '@tanstack/react-query';
import { ApiError, apiRequest } from '../auth/apiRequest';
import { ActivityReportSchema, type ActivityReport } from '../types/schemas';

/** The daemon capability that advertises resources and the activity route. */
export const SYSTEM_VISIBILITY_CAPABILITY = 'system_visibility';

/** How often a live run's activity is re-read (the daemon samples every 2s). */
export const ACTIVITY_POLL_MS = 5_000;

export const RUN_ACTIVITY_KEY = (identifier: string, runId: string) =>
  ['runActivity', identifier, runId] as const;

/**
 * The activity of the issue's run `runId` (its latest run when empty).
 * Resolves to null when nothing is recorded (404); polls while `live`.
 */
export function useRunActivity(
  identifier: string,
  runId: string,
  opts: { enabled: boolean; live: boolean },
) {
  return useQuery({
    queryKey: RUN_ACTIVITY_KEY(identifier, runId),
    enabled: opts.enabled && identifier !== '',
    queryFn: async (): Promise<ActivityReport | null> => {
      const q = runId ? `?run=${encodeURIComponent(runId)}` : '';
      try {
        const { data } = await apiRequest(
          `/api/v1/issues/${encodeURIComponent(identifier)}/activity${q}`,
          { op: 'activity fetch' },
        );
        return ActivityReportSchema.parse(data);
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }
    },
    refetchInterval: opts.live ? ACTIVITY_POLL_MS : false,
    staleTime: opts.live ? 0 : 30_000,
  });
}
