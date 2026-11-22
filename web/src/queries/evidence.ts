// Evidence report — what ran, what was checked, who approved, for one
// issue. Read-only (viewer scope; the daemon redacts it for the caller's
// token). Every request goes through apiRequest → authedFetch.

import { useQuery } from '@tanstack/react-query';
import { ApiError, apiRequest } from '../auth/apiRequest';
import { EvidenceReportSchema, type EvidenceReport } from '../types/schemas';

export const EVIDENCE_KEY = (identifier: string) => ['evidence', identifier] as const;

/** StateSnapshot.capabilities entry of a daemon that serves the report. */
export const EVIDENCE_CAPABILITY = 'issue_evidence';

/** How often an open report re-reads while the issue has a live run. */
export const EVIDENCE_POLL_MS = 10_000;
const EVIDENCE_STALE_MS = 30_000;

/**
 * The issue's evidence report. Resolves to null when the daemon does not
 * serve it (503 not_supported).
 */
export function useIssueEvidence(identifier: string, live: boolean) {
  return useQuery({
    queryKey: EVIDENCE_KEY(identifier),
    enabled: identifier !== '',
    queryFn: async (): Promise<EvidenceReport | null> => {
      try {
        const { data } = await apiRequest(
          `/api/v1/issues/${encodeURIComponent(identifier)}/evidence`,
          { op: 'evidence report fetch' },
        );
        return EvidenceReportSchema.parse(data);
      } catch (err) {
        if (err instanceof ApiError && err.status === 503 && err.code === 'not_supported') {
          return null;
        }
        throw err;
      }
    },
    refetchInterval: live ? EVIDENCE_POLL_MS : false,
    staleTime: live ? 0 : EVIDENCE_STALE_MS,
  });
}
