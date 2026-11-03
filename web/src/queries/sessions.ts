// Sessions panel (#178): the daemon's agent sessions and operator messages.
//
// GET  /api/v1/sessions                 — sessions, waiting-for-input first
// POST /api/v1/sessions/{id}/message    — queue a message for a running session
// GET  /api/v1/sessions/{id}/transcript — a hand-started session's
//                                         transcript, paged backwards
// tmux control and "New session" live in queries/sessionsTmux.ts.
//
// A session waiting for input takes a reply through the existing
// provide-input path (useProvideInput in queries/issues.ts), not here.

import { useMemo } from 'react';
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';
import {
  SessionTranscriptSchema,
  SessionsResponseSchema,
  SteeringPhrasesResponseSchema,
  type SessionRow,
  type SteeringPhrase,
  type SessionsResponse,
  type SessionTranscript,
  type TranscriptEntry,
} from '../types/schemas';

export const SESSIONS_KEY = ['sessions'] as const;

/** How often the open Sessions page re-reads the list. */
export const SESSIONS_REFETCH_MS = 2_000;

const EMPTY_SESSIONS: SessionRow[] = [];

export function useSessions() {
  const q = useQuery({
    queryKey: SESSIONS_KEY,
    queryFn: async (): Promise<SessionsResponse> => {
      const { data } = await apiRequest('/api/v1/sessions', { op: 'sessions' });
      return SessionsResponseSchema.parse(data);
    },
    refetchInterval: SESSIONS_REFETCH_MS,
    retry: (failureCount, err) => !(err instanceof UnauthorizedError) && failureCount < 2,
  });
  return {
    ...q,
    sessions: q.data?.sessions ?? EMPTY_SESSIONS,
    // undefined until the daemon answered, or from a daemon without tmux
    // control: the panel then neither warns nor offers New session.
    tmux: q.data?.tmux,
    newSession: q.data?.newSession,
  };
}

/** The key a session is addressed by: its CLI session id, else its run id. */
export function sessionKey(s: Pick<SessionRow, 'id' | 'runId'>): string {
  return s.id || s.runId || '';
}

/**
 * How a session is named in the panel: its issue, else (a session started
 * by hand) its CLI title, else its directory's name, else its id.
 */
export function sessionLabel(s: Pick<SessionRow, 'identifier' | 'title' | 'cwd' | 'id'>): string {
  if (s.identifier) return s.identifier;
  if (s.title) return s.title;
  const dir = s.cwd?.split('/').filter(Boolean).pop();
  return dir ?? (s.id.slice(0, 8) || 'session');
}

export function useSendSessionMessage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      id,
      message,
      submit,
    }: {
      id: string;
      message: string;
      submit?: boolean;
    }) => {
      await apiRequest(`/api/v1/sessions/${encodeURIComponent(id)}/message`, {
        op: 'sessionMessage',
        method: 'POST',
        json: submit === undefined ? { message } : { message, submit },
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

/** How often an open transcript re-reads its newest page. */
export const TRANSCRIPT_REFETCH_MS = 3_000;
/** Entries per transcript page. */
export const TRANSCRIPT_PAGE_SIZE = 100;

const EMPTY_ENTRIES: TranscriptEntry[] = [];

/**
 * The transcript of a session started by hand,
 * newest page first in the cache; `entries` is oldest first. `fetchNextPage`
 * loads the next older page.
 */
export function useSessionTranscript(id: string) {
  const q = useInfiniteQuery({
    queryKey: [...SESSIONS_KEY, 'transcript', id],
    enabled: id !== '',
    initialPageParam: undefined as number | undefined,
    queryFn: async ({ pageParam }): Promise<SessionTranscript> => {
      const params = new URLSearchParams({ limit: String(TRANSCRIPT_PAGE_SIZE) });
      if (pageParam !== undefined) params.set('before', String(pageParam));
      const { data } = await apiRequest(
        `/api/v1/sessions/${encodeURIComponent(id)}/transcript?${params.toString()}`,
        { op: 'sessionTranscript' },
      );
      return SessionTranscriptSchema.parse(data);
    },
    getNextPageParam: (last) => (last.hasMore ? last.before : undefined),
    refetchInterval: TRANSCRIPT_REFETCH_MS,
    retry: (failureCount, err) => !(err instanceof UnauthorizedError) && failureCount < 2,
  });
  const pages = q.data?.pages;
  const entries = useMemo(
    () => (pages === undefined ? EMPTY_ENTRIES : [...pages].reverse().flatMap((p) => p.entries)),
    [pages],
  );
  return { ...q, entries };
}

const EMPTY_PHRASES: SteeringPhrase[] = [];

/**
 * #218 — the one-click steering chips (sessions.steering_phrases). Config,
 * read once per page load; an older daemon without the route shows none.
 */
export function useSteeringPhrases(): SteeringPhrase[] {
  const q = useQuery({
    queryKey: ['steeringPhrases'] as const,
    queryFn: async (): Promise<SteeringPhrase[]> => {
      const { data } = await apiRequest('/api/v1/steering/phrases', { op: 'steering phrases' });
      return SteeringPhrasesResponseSchema.parse(data).phrases;
    },
    staleTime: Infinity,
    retry: false,
  });
  return q.data ?? EMPTY_PHRASES;
}
