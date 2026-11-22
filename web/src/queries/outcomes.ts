// Run and PR outcomes (cost and outcome visibility): GET /api/v1/outcomes,
// success rate, PRs merged, median wall time and tokens and retry rate per
// profile and per playbook over a window. Read-only, through apiRequest →
// authedFetch.

import { useQuery } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { OutcomesResponseSchema, type OutcomesResponse } from '../types/schemas';

export const OUTCOME_WINDOWS = ['7d', '30d'] as const;
export type OutcomeWindow = (typeof OUTCOME_WINDOWS)[number];

const OUTCOMES_STALE_MS = 30_000;

export function useOutcomes(span: OutcomeWindow) {
  return useQuery({
    queryKey: ['outcomes', span] as const,
    queryFn: async (): Promise<OutcomesResponse> => {
      const { data } = await apiRequest(`/api/v1/outcomes?window=${span}`, {
        op: 'outcomes fetch',
      });
      return OutcomesResponseSchema.parse(data);
    },
    staleTime: OUTCOMES_STALE_MS,
  });
}
