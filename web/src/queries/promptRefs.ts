// Backend-aware skills (#123): the prompt editor's reference check and the
// per-profile backend map the pickers use. Both read the daemon's #86
// validator through the API, so the dashboard and `itervox doctor` agree.

import { useQuery } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import {
  ProfileBackendsSchema,
  PromptRefCheckSchema,
  type ProfileBackends,
  type PromptRefCheck,
} from '../types/schemas';

export const PROFILE_BACKENDS_KEY = ['skills', 'profile-backends'] as const;

export interface PromptRefCheckRequest {
  /** Profile name, used only in messages. */
  profile: string;
  /** The draft profile's runner command and backend. */
  command: string;
  backend: string;
  /** One prompt file's text (SOUL.md or INSTRUCTIONS.md). */
  text: string;
}

/** Runs the reference validator over one draft prompt. Throws on any failure. */
export async function checkPromptRefs(
  req: PromptRefCheckRequest,
  signal?: AbortSignal,
): Promise<PromptRefCheck> {
  const { data } = await apiRequest('/api/v1/skills/check-refs', {
    op: 'reference check',
    method: 'POST',
    json: req,
    signal,
    retryOnBusy: false,
  });
  return PromptRefCheckSchema.parse(data);
}

/** The backend each configured profile runs on, resolved by the daemon. */
export function useProfileBackends() {
  return useQuery({
    queryKey: PROFILE_BACKENDS_KEY,
    queryFn: async (): Promise<ProfileBackends> => {
      const { data } = await apiRequest('/api/v1/skills/profile-backends', {
        op: 'profile backends fetch',
      });
      return ProfileBackendsSchema.parse(data);
    },
    staleTime: 10_000,
    retry: (failureCount, err) => {
      if (err instanceof UnauthorizedError) return false;
      return failureCount < 2;
    },
  });
}
