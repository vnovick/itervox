import { useQuery } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { HUB_API_BASE } from '../auth/apiBase';
import { HubProjectsResponseSchema, type HubProjectsResponse } from '../types/schemas';

// #228 — the hub's member list. The path is absolute (/hub/api/v1/...), so
// it is not remapped by apiUrl: the switcher reads the hub itself even from
// a project dashboard served under /hub/p/{project}/.
export const HUB_PROJECTS_KEY = ['hub', 'projects'] as const;

export function useHubProjects(enabled = true) {
  return useQuery<HubProjectsResponse>({
    queryKey: HUB_PROJECTS_KEY,
    queryFn: async () => {
      const { data } = await apiRequest(`${HUB_API_BASE}/projects`, { op: 'fetch hub projects' });
      return HubProjectsResponseSchema.parse(data);
    },
    enabled,
    staleTime: 10_000,
    refetchInterval: 15_000,
  });
}

/** The dashboard URL of a hub project. */
export function hubProjectHref(name: string): string {
  return `/hub/p/${encodeURIComponent(name)}/`;
}
