import { useQuery } from '@tanstack/react-query';
import { z } from 'zod';
import { apiRequest } from '../auth/apiRequest';

const ProjectSchema = z.object({
  id: z.string(),
  name: z.string(),
  slug: z.string(),
});

const ProjectsResponseSchema = z.object({
  projects: z.array(ProjectSchema),
});

export type Project = z.infer<typeof ProjectSchema>;

export const PROJECTS_KEY = ['projects'] as const;

export function useProjects(enabled = true) {
  return useQuery({
    queryKey: PROJECTS_KEY,
    queryFn: async () => {
      const { data } = await apiRequest('/api/v1/projects', { op: 'fetch projects' });
      return ProjectsResponseSchema.parse(data).projects;
    },
    enabled,
    staleTime: 60_000,
  });
}
