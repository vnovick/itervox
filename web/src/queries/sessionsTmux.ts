// Sessions panel, tmux (#178): the screen and keys of sessions running in a
// tmux pane, "New session", and stopping sessions.
//
// GET    /api/v1/sessions/{id}/screen — the pane's text (polled while open)
// POST   /api/v1/sessions/{id}/keys   — one allowlisted key
// POST   /api/v1/sessions              — start an interactive CLI in tmux
// POST   /api/v1/sessions/{id}/stop    — stop a session (graceful, or force)
//
// Text goes through useSendSessionMessage (queries/sessions.ts) with
// `submit`.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';
import {
  SessionScreenSchema,
  SessionRowSchema,
  SessionStopResultSchema,
  type SessionRow,
} from '../types/schemas';
import { SESSIONS_KEY } from './sessions';

/** How often an open, visible tmux screen is re-captured. */
export const SCREEN_REFETCH_MS = 1_000;

/** The keys the daemon accepts (internal/tmuxctl AllowedKeys). */
export type SessionKey = 'Enter' | 'Escape' | 'Up' | 'Down' | 'Tab' | 'C-c' | '1' | '2' | '3';

function toastError(prefix: string, err: unknown) {
  if (err instanceof UnauthorizedError) return; // AuthGate handles the UI
  const message = err instanceof Error ? err.message : 'unknown error';
  useToastStore.getState().addToast(`${prefix}: ${message}`, 'error');
}

/**
 * The pane of a controllable session, re-captured every second while the
 * tab is visible (TanStack Query pauses the interval in a hidden tab).
 */
export function useSessionScreen(id: string, enabled: boolean) {
  return useQuery({
    queryKey: [...SESSIONS_KEY, 'screen', id],
    enabled: enabled && id !== '',
    queryFn: async () => {
      const { data } = await apiRequest(
        `/api/v1/sessions/${encodeURIComponent(id)}/screen?lines=200`,
        { op: 'sessionScreen' },
      );
      return SessionScreenSchema.parse(data);
    },
    refetchInterval: SCREEN_REFETCH_MS,
    refetchIntervalInBackground: false,
    retry: false,
  });
}

export function useSendSessionKey() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, key }: { id: string; key: SessionKey }) => {
      await apiRequest(`/api/v1/sessions/${encodeURIComponent(id)}/keys`, {
        op: 'sessionKey',
        method: 'POST',
        json: { key },
      });
    },
    onSuccess: (_d, { id }) => {
      void queryClient.invalidateQueries({ queryKey: [...SESSIONS_KEY, 'screen', id] });
    },
    onError: (err: unknown) => {
      toastError('Key not sent', err);
    },
  });
}

export interface NewSessionInput {
  backend: 'claude' | 'codex';
  cwd?: string;
  profile?: string;
  prompt?: string;
}

export function useStartSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: NewSessionInput): Promise<SessionRow> => {
      const { data } = await apiRequest('/api/v1/sessions', {
        op: 'startSession',
        method: 'POST',
        json: input,
      });
      return SessionRowSchema.parse((data as { session: unknown }).session);
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: SESSIONS_KEY });
    },
    onError: (err: unknown) => {
      toastError('Session not started', err);
    },
  });
}

const STOP_TOAST: Record<string, string> = {
  stopped: 'Session stopped.',
  killed: 'Session killed (tmux kill-session).',
  interrupted: 'Ctrl-C sent — the session is still running.',
};

/**
 * Stops a session: the CLI's interrupt keys, then — only for a tmux
 * session itervox started — kill-session if it is still there after a few
 * seconds. `force` kills an itervox session at once.
 */
export function useStopSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, force }: { id: string; force: boolean }) => {
      const { data } = await apiRequest(`/api/v1/sessions/${encodeURIComponent(id)}/stop`, {
        op: 'stopSession',
        method: 'POST',
        json: { force },
      });
      return SessionStopResultSchema.parse(data);
    },
    onSuccess: (res) => {
      const message = STOP_TOAST[res.outcome] ?? `Session ${res.outcome}.`;
      useToastStore
        .getState()
        .addToast(message, res.outcome === 'interrupted' ? 'info' : 'success');
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: SESSIONS_KEY });
    },
    onError: (err: unknown) => {
      toastError('Session not stopped', err);
    },
  });
}
