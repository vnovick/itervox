// Sessions panel (#178): the daemon's agent sessions and operator messages.
//
// GET  /api/v1/sessions               — sessions, waiting-for-input first
// POST /api/v1/sessions/{id}/message  — queue a message for a running session
//
// A session waiting for input takes a reply through the existing
// provide-input path (useProvideInput in queries/issues.ts), not here.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';
import { SessionsResponseSchema, type SessionRow } from '../types/schemas';

export const SESSIONS_KEY = ['sessions'] as const;

/** How often the open Sessions page re-reads the list. */
export const SESSIONS_REFETCH_MS = 2_000;

const EMPTY_SESSIONS: SessionRow[] = [];

export function useSessions() {
  const q = useQuery({
    queryKey: SESSIONS_KEY,
    queryFn: async (): Promise<SessionRow[]> => {
      const { data } = await apiRequest('/api/v1/sessions', { op: 'sessions' });
      return SessionsResponseSchema.parse(data).sessions;
    },
    refetchInterval: SESSIONS_REFETCH_MS,
    retry: (failureCount, err) => !(err instanceof UnauthorizedError) && failureCount < 2,
  });
  return { ...q, sessions: q.data ?? EMPTY_SESSIONS };
}

/** The key a session is addressed by: its CLI session id, else its run id. */
export function sessionKey(s: Pick<SessionRow, 'id' | 'runId'>): string {
  return s.id || s.runId || '';
}

export function useSendSessionMessage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, message }: { id: string; message: string }) => {
      await apiRequest(`/api/v1/sessions/${encodeURIComponent(id)}/message`, {
        op: 'sessionMessage',
        method: 'POST',
        json: { message },
      });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: SESSIONS_KEY });
    },
    onError: (err: unknown) => {
      if (err instanceof UnauthorizedError) return; // AuthGate handles the UI
      const message = err instanceof Error ? err.message : 'unknown error';
      useToastStore.getState().addToast(`Message not sent: ${message}`, 'error');
      void queryClient.invalidateQueries({ queryKey: SESSIONS_KEY });
    },
  });
}
