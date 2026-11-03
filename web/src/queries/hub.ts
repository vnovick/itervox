import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { HUB_API_BASE } from '../auth/apiBase';
import {
  HubNotificationsResponseSchema,
  HubProjectsResponseSchema,
  type HubNotificationSettings,
  type HubNotificationsResponse,
  type HubProjectsResponse,
} from '../types/schemas';

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

// #229 — the hub's notification settings (admin) and the per-channel test
// (operator). Absolute /hub/api/v1 paths, like the project list.
export const HUB_NOTIFICATIONS_KEY = ['hub', 'notifications'] as const;

export function useHubNotifications() {
  return useQuery<HubNotificationsResponse>({
    queryKey: HUB_NOTIFICATIONS_KEY,
    queryFn: async () => {
      const { data } = await apiRequest(`${HUB_API_BASE}/notifications`, {
        op: 'fetch notification settings',
      });
      return HubNotificationsResponseSchema.parse(data);
    },
    staleTime: 10_000,
  });
}

export function useSaveHubNotifications() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (settings: HubNotificationSettings) => {
      const { data } = await apiRequest(`${HUB_API_BASE}/notifications`, {
        op: 'save notification settings',
        method: 'PUT',
        json: settings,
      });
      return HubNotificationsResponseSchema.parse(data);
    },
    onSuccess: (resp) => {
      qc.setQueryData(HUB_NOTIFICATIONS_KEY, resp);
    },
  });
}

export function useTestHubNotification() {
  return useMutation({
    mutationFn: async (channel: string) => {
      await apiRequest(`${HUB_API_BASE}/notifications/test`, {
        op: `send a test ${channel} notification`,
        method: 'POST',
        json: { channel },
      });
      return channel;
    },
  });
}
