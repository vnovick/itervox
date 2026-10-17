import { useQuery } from '@tanstack/react-query';
import { z } from 'zod';
import { apiRequest } from '../auth/apiRequest';

// #182 — the choices of the combined tracker scope filter (Linear only).

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
