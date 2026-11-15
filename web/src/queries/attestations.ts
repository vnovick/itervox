// Signed checks (0.3.3) — the issue's signed check records, read and
// verified by the daemon on the HTTP goroutine. Read-only (viewer scope);
// every request goes through apiRequest → authedFetch.

import { useQuery } from '@tanstack/react-query';
import { ApiError, apiRequest } from '../auth/apiRequest';
import { AttestationsResponseSchema, type AttestationsResponse } from '../types/schemas';

export const ATTESTATIONS_KEY = (identifier: string) => ['attestations', identifier] as const;

/** How often an open issue re-reads its records while a run is live. */
export const ATTESTATIONS_POLL_MS = 10_000;
const ATTESTATIONS_STALE_MS = 30_000;

/**
 * Every run's signed check records for the issue, oldest run first.
 * Resolves to null when the daemon does not serve signed checks
 * (503 not_supported).
 */
export function useIssueAttestations(identifier: string, live: boolean) {
  return useQuery({
    queryKey: ATTESTATIONS_KEY(identifier),
    enabled: identifier !== '',
    queryFn: async (): Promise<AttestationsResponse | null> => {
      try {
        const { data } = await apiRequest(
          `/api/v1/issues/${encodeURIComponent(identifier)}/attestations`,
          { op: 'signed checks fetch' },
        );
        return AttestationsResponseSchema.parse(data);
      } catch (err) {
        if (err instanceof ApiError && err.status === 503 && err.code === 'not_supported') {
          return null;
        }
        throw err;
      }
    },
    refetchInterval: live ? ATTESTATIONS_POLL_MS : false,
    staleTime: live ? 0 : ATTESTATIONS_STALE_MS,
  });
}
