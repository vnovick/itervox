// #251 — New task and saved task templates. Every request goes through
// apiRequest → authedFetch; errors carry the server's {code,message,field}
// so the form can pin them to an input.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';
import { useItervoxStore } from '../store/itervoxStore';
import {
  TaskCreateResultSchema,
  TaskTemplatesResponseSchema,
  type TaskCreateRequest,
  type TaskCreateResult,
  type TaskTemplate,
  type TaskTemplatesResponse,
} from '../types/schemas';
import { ISSUES_KEY, ISSUE_KEY } from './issues';

export const TASK_TEMPLATES_KEY = ['task-templates'] as const;

// Template files change only when someone saves or edits one.
const TASK_TEMPLATES_STALE_MS = 30_000;

function toastError(err: unknown, fallback: string): void {
  if (err instanceof UnauthorizedError) return; // AuthGate handles the UI
  useToastStore.getState().addToast(err instanceof Error ? err.message : fallback, 'error');
}

export function useTaskTemplates(enabled = true) {
  return useQuery({
    queryKey: TASK_TEMPLATES_KEY,
    enabled,
    queryFn: async (): Promise<TaskTemplatesResponse> => {
      const { data } = await apiRequest('/api/v1/task-templates', { op: 'task templates fetch' });
      return TaskTemplatesResponseSchema.parse(data);
    },
    staleTime: TASK_TEMPLATES_STALE_MS,
  });
}

/**
 * POST /api/v1/tasks. No optimistic card: the identifier comes from the
 * daemon. Errors are left to the caller (the form shows field errors
 * inline).
 */
export function useCreateTask() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (req: TaskCreateRequest): Promise<TaskCreateResult> => {
      const { data } = await apiRequest('/api/v1/tasks', {
        op: 'create task',
        method: 'POST',
        json: req,
      });
      return TaskCreateResultSchema.parse(data);
    },
    onSuccess: (res) => {
      useToastStore
        .getState()
        .addToast(
          res.started
            ? `Created ${res.identifier}; dispatch requested.`
            : `Created ${res.identifier} in ${res.state}.`,
          'success',
        );
      void useItervoxStore.getState().refreshSnapshot();
      void queryClient.invalidateQueries({ queryKey: ISSUES_KEY });
    },
  });
}

/** PUT /api/v1/tasks/{identifier}/instructions, until the task starts. */
export function useSetTaskInstructions() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      identifier,
      instructions,
    }: {
      identifier: string;
      instructions: string;
    }) => {
      await apiRequest(`/api/v1/tasks/${encodeURIComponent(identifier)}/instructions`, {
        op: 'save task instructions',
        method: 'PUT',
        json: { instructions },
      });
    },
    onSuccess: (_data, { identifier }) => {
      useToastStore.getState().addToast('Task instructions saved.', 'success');
      void queryClient.invalidateQueries({ queryKey: ISSUE_KEY(identifier) });
    },
    onError: (err) => {
      toastError(err, 'Saving the instructions failed.');
    },
  });
}

/** POST /api/v1/task-templates/{name} (admin): save the form as a template. */
export function useSaveTaskTemplate() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (t: Omit<TaskTemplate, 'vars'>) => {
      await apiRequest(`/api/v1/task-templates/${encodeURIComponent(t.name)}`, {
        op: 'save task template',
        method: 'POST',
        json: t,
      });
    },
    onSuccess: (_data, t) => {
      useToastStore.getState().addToast(`Template ${t.name} saved.`, 'success');
      void queryClient.invalidateQueries({ queryKey: TASK_TEMPLATES_KEY });
    },
    onError: (err) => {
      toastError(err, 'Saving the template failed.');
    },
  });
}
