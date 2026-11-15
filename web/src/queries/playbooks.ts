// #218 — task-type playbooks and the per-run decision logs. Read-only here
// (the per-issue override mutation is useSetIssuePlaybook in issues.ts);
// every request goes through apiRequest → authedFetch.

import { useQuery } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import {
  DecisionsResponseSchema,
  PlaybooksResponseSchema,
  type DecisionsResponse,
  type PlaybooksResponse,
} from '../types/schemas';

export const PLAYBOOKS_KEY = ['playbooks'] as const;
export const DECISIONS_KEY = (identifier: string) => ['decisions', identifier] as const;

// The playbook files change only when someone edits them.
const PLAYBOOKS_STALE_MS = 60_000;
/** How often an open issue re-reads a live run's decision log. */
export const DECISIONS_POLL_MS = 5_000;

export function usePlaybooks() {
  return useQuery({
    queryKey: PLAYBOOKS_KEY,
    queryFn: async (): Promise<PlaybooksResponse> => {
      const { data } = await apiRequest('/api/v1/playbooks', { op: 'playbooks fetch' });
      return PlaybooksResponseSchema.parse(data);
    },
    staleTime: PLAYBOOKS_STALE_MS,
  });
}

/** Every run's decision log for the issue; polls while a run is live. */
export function useIssueDecisions(identifier: string, live: boolean) {
  return useQuery({
    queryKey: DECISIONS_KEY(identifier),
    enabled: identifier !== '',
    queryFn: async (): Promise<DecisionsResponse> => {
      const { data } = await apiRequest(
        `/api/v1/issues/${encodeURIComponent(identifier)}/decisions`,
        { op: 'decisions fetch' },
      );
      return DecisionsResponseSchema.parse(data);
    },
    refetchInterval: live ? DECISIONS_POLL_MS : false,
  });
}
