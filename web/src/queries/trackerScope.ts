import { useQuery } from '@tanstack/react-query';
import { z } from 'zod';
import { apiRequest } from '../auth/apiRequest';
import { FilterGroupSchema, type FilterGroup } from '../types/schemas';

// #182 — the choices of the combined tracker scope filter; #198 — the
// tracker filter (groups of conditions) and its read-only preview.

const LabelsResponseSchema = z.object({ labels: z.array(z.string()) });

const TrackerUserSchema = z.object({
  id: z.string(),
  name: z.string(),
  email: z.string().optional(),
});

const UsersResponseSchema = z.object({ users: z.array(TrackerUserSchema) });

export type TrackerUser = z.infer<typeof TrackerUserSchema>;

export function useTrackerLabels(enabled = true) {
  return useQuery({
    queryKey: ['tracker', 'labels'] as const,
    queryFn: async () => {
      const { data } = await apiRequest('/api/v1/tracker/labels', { op: 'fetch tracker labels' });
      return LabelsResponseSchema.parse(data).labels;
    },
    enabled,
    staleTime: 60_000,
  });
}

export function useTrackerUsers(enabled = true) {
  return useQuery({
    queryKey: ['tracker', 'users'] as const,
    queryFn: async () => {
      const { data } = await apiRequest('/api/v1/tracker/users', { op: 'fetch tracker users' });
      return UsersResponseSchema.parse(data).users;
    },
    enabled,
    staleTime: 60_000,
  });
}

const TrackerTeamSchema = z.object({ id: z.string(), key: z.string(), name: z.string() });
const TeamsResponseSchema = z.object({ teams: z.array(TrackerTeamSchema) });
export type TrackerTeam = z.infer<typeof TrackerTeamSchema>;

export function useTrackerTeams(enabled = true) {
  return useQuery({
    queryKey: ['tracker', 'teams'] as const,
    queryFn: async () => {
      const { data } = await apiRequest('/api/v1/tracker/teams', { op: 'fetch tracker teams' });
      return TeamsResponseSchema.parse(data).teams;
    },
    enabled,
    staleTime: 60_000,
  });
}

const TrackerScopeResponseSchema = z.object({
  scope: z.object({
    filter: z.array(FilterGroupSchema).nullable().optional(),
    filterMode: z.boolean().optional(),
    conditions: z.array(z.string()).nullable().optional(),
  }),
});

export const TRACKER_SCOPE_KEY = ['tracker', 'scope'] as const;

/** The effective tracker filter (the legacy keys as one group) and the conditions the tracker supports. */
export function useTrackerScope(enabled = true) {
  return useQuery({
    queryKey: TRACKER_SCOPE_KEY,
    queryFn: async () => {
      const { data } = await apiRequest('/api/v1/tracker/scope', { op: 'fetch tracker scope' });
      const { scope } = TrackerScopeResponseSchema.parse(data);
      return {
        filter: scope.filter ?? [],
        filterMode: scope.filterMode ?? false,
        conditions: scope.conditions ?? [],
      };
    },
    enabled,
    staleTime: 10_000,
  });
}

const FilterPreviewSchema = z.object({ count: z.number(), identifiers: z.array(z.string()) });
export type FilterPreview = z.infer<typeof FilterPreviewSchema>;

/** POST /api/v1/tracker/filter/preview — read-only: what `groups` would read now. */
export function useFilterPreview(groups: FilterGroup[], enabled = true) {
  return useQuery({
    queryKey: ['tracker', 'filter-preview', JSON.stringify(groups)] as const,
    queryFn: async () => {
      const { data } = await apiRequest('/api/v1/tracker/filter/preview', {
        op: 'preview tracker filter',
        method: 'POST',
        json: { filter: groups },
      });
      return FilterPreviewSchema.parse(data);
    },
    enabled,
    staleTime: 15_000,
    retry: false,
  });
}
