import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { QueryClient } from '@tanstack/react-query';
import { useItervoxStore } from '../store/itervoxStore';
import { useToastStore } from '../store/toastStore';
import type { StateSnapshot, TrackerIssue } from '../types/schemas';
import { TrackerIssueSchema } from '../types/schemas';
import { z } from 'zod';
import { authedFetch } from '../auth/authedFetch';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { logIdentifiersKey, logsKey, sublogsKey } from './logs';

export const ISSUES_KEY = ['issues'] as const;
export const ISSUE_KEY = (identifier: string) => ['issue', identifier] as const;

type RollbackContext =
  | {
      prevIssue?: TrackerIssue;
      prevIssueIdentifier?: string;
      prevIssues?: TrackerIssue[];
      prevSnapshot?: StateSnapshot;
    }
  | undefined;

/**
 * Extracts a user-facing message from an unknown error and shows it as a toast.
 * Silently drops `UnauthorizedError` — the AuthGate swaps to the login screen
 * instead; a toast on top of that would be noise.
 */
function toastApiError(err: unknown, fallback = 'Action failed — please try again.'): void {
  if (err instanceof UnauthorizedError) return;
  const message = err instanceof Error ? err.message : fallback;
  useToastStore.getState().addToast(message);
}

/**
 * Returns an `onError` handler that rolls back optimistic query/snapshot updates
 * and surfaces the error to the user via a toast notification.
 * Used by all issue mutations that apply optimistic updates.
 */
function makeRollbackHandler(queryClient: QueryClient) {
  return (_error: unknown, _vars: unknown, context: RollbackContext) => {
    if (context?.prevIssues) queryClient.setQueryData(ISSUES_KEY, context.prevIssues);
    if (context?.prevIssueIdentifier && context.prevIssue) {
      queryClient.setQueryData(ISSUE_KEY(context.prevIssueIdentifier), context.prevIssue);
    }
    if (context?.prevSnapshot) useItervoxStore.getState().setSnapshot(context.prevSnapshot);
    toastApiError(_error);
  };
}

function invalidateIssueQueries(queryClient: QueryClient, identifier?: string): void {
  void queryClient.invalidateQueries({ queryKey: ISSUES_KEY });
  if (identifier) {
    void queryClient.invalidateQueries({ queryKey: ISSUE_KEY(identifier) });
  }
}

function refreshIssueViews(queryClient: QueryClient, identifier?: string): void {
  void useItervoxStore.getState().refreshSnapshot();
  invalidateIssueQueries(queryClient, identifier);
}

function updateIssueCaches(
  queryClient: QueryClient,
  identifier: string,
  updater: (issue: TrackerIssue) => TrackerIssue,
): { prevIssue?: TrackerIssue; prevIssues?: TrackerIssue[] } {
  const prevIssues = queryClient.getQueryData<TrackerIssue[]>(ISSUES_KEY);
  if (prevIssues) {
    queryClient.setQueryData<TrackerIssue[]>(
      ISSUES_KEY,
      prevIssues.map((issue) => (issue.identifier === identifier ? updater(issue) : issue)),
    );
  }

  const prevIssue = queryClient.getQueryData<TrackerIssue>(ISSUE_KEY(identifier));
  if (prevIssue) {
    queryClient.setQueryData<TrackerIssue>(ISSUE_KEY(identifier), updater(prevIssue));
  }

  return { prevIssue, prevIssues };
}

async function fetchIssues(): Promise<TrackerIssue[]> {
  const res = await authedFetch('/api/v1/issues');
  if (!res.ok) throw new Error(`fetch issues failed: ${String(res.status)}`);
  return z.array(TrackerIssueSchema).parse(await res.json());
}

export function useIssues() {
  return useQuery({
    queryKey: ISSUES_KEY,
    queryFn: fetchIssues,
    staleTime: 5_000,
  });
}

export function useInvalidateIssues() {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: ISSUES_KEY });
}

export function useIssue(identifier: string) {
  return useQuery({
    queryKey: ISSUE_KEY(identifier),
    queryFn: async () => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}`);
      if (!res.ok) throw new Error(`fetch issue failed: ${String(res.status)}`);
      return TrackerIssueSchema.parse(await res.json());
    },
    enabled: identifier !== '',
    staleTime: 0,
  });
}

function optimisticPauseSnapshot(snapshot: StateSnapshot, identifier: string): StateSnapshot {
  const wasRunning = snapshot.running.some((row) => row.identifier === identifier);
  const wasRetrying = snapshot.retrying.some((row) => row.identifier === identifier);
  const alreadyPaused = snapshot.paused.includes(identifier);
  if (!wasRunning && !wasRetrying && alreadyPaused) {
    return snapshot;
  }
  // Note: `pausedWithPR` is intentionally not updated here. The PR URL is not
  // known client-side at optimistic-update time; it will appear once the next
  // real snapshot arrives from the server.
  return {
    ...snapshot,
    running: snapshot.running.filter((row) => row.identifier !== identifier),
    retrying: snapshot.retrying.filter((row) => row.identifier !== identifier),
    paused: alreadyPaused ? snapshot.paused : [...snapshot.paused, identifier],
    counts: {
      ...snapshot.counts,
      running: wasRunning ? Math.max(0, snapshot.counts.running - 1) : snapshot.counts.running,
      paused:
        !alreadyPaused && (wasRunning || wasRetrying)
          ? snapshot.counts.paused + 1
          : snapshot.counts.paused,
    },
  };
}

export function useUpdateIssueState() {
  const queryClient = useQueryClient();
  return useMutation({
    onMutate: async ({ identifier, state }: { identifier: string; state: string }) => {
      // Cancel any in-flight refetches so they don't overwrite the optimistic update.
      await queryClient.cancelQueries({ queryKey: ISSUES_KEY });
      await queryClient.cancelQueries({ queryKey: ISSUE_KEY(identifier) });
      const { prevIssue, prevIssues } = updateIssueCaches(queryClient, identifier, (issue) => ({
        ...issue,
        state,
      }));

      return { prevIssue, prevIssueIdentifier: identifier, prevIssues };
    },
    mutationFn: async ({ identifier, state }: { identifier: string; state: string }) => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/state`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ state }),
      });
      if (!res.ok) throw new Error(`updateIssueState failed: ${String(res.status)}`);
    },
    onError: makeRollbackHandler(queryClient),
    onSuccess: (_data, { identifier }) => {
      invalidateIssueQueries(queryClient, identifier);
    },
  });
}

export function useSetIssueProfile() {
  const queryClient = useQueryClient();
  return useMutation({
    onMutate: async ({ identifier, profile }: { identifier: string; profile: string }) => {
      await queryClient.cancelQueries({ queryKey: ISSUES_KEY });
      await queryClient.cancelQueries({ queryKey: ISSUE_KEY(identifier) });
      const { prevIssue, prevIssues } = updateIssueCaches(queryClient, identifier, (issue) => ({
        ...issue,
        agentProfile: profile || undefined,
      }));
      return { prevIssue, prevIssueIdentifier: identifier, prevIssues };
    },
    mutationFn: async ({ identifier, profile }: { identifier: string; profile: string }) => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/profile`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ profile }),
      });
      if (!res.ok) throw new Error(`setIssueProfile failed: ${String(res.status)}`);
    },
    onError: makeRollbackHandler(queryClient),
    onSuccess: (_data, { identifier }) => {
      invalidateIssueQueries(queryClient, identifier);
    },
  });
}

export function useSetIssueBackend() {
  const queryClient = useQueryClient();
  return useMutation({
    onMutate: async ({ identifier, backend }: { identifier: string; backend: string }) => {
      await queryClient.cancelQueries({ queryKey: ISSUES_KEY });
      await queryClient.cancelQueries({ queryKey: ISSUE_KEY(identifier) });
      const { prevIssue, prevIssues } = updateIssueCaches(queryClient, identifier, (issue) => ({
        ...issue,
        agentBackend: backend || undefined,
      }));
      return { prevIssue, prevIssueIdentifier: identifier, prevIssues };
    },
    mutationFn: async ({ identifier, backend }: { identifier: string; backend: string }) => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/backend`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ backend }),
      });
      if (!res.ok) throw new Error(`setIssueBackend failed: ${String(res.status)}`);
    },
    onError: makeRollbackHandler(queryClient),
    onSuccess: (_data, { identifier }) => {
      invalidateIssueQueries(queryClient, identifier);
    },
  });
}

export function useCancelIssue() {
  const queryClient = useQueryClient();
  return useMutation({
    onMutate: async (identifier: string) => {
      await queryClient.cancelQueries({ queryKey: ISSUES_KEY });
      await queryClient.cancelQueries({ queryKey: ISSUE_KEY(identifier) });
      const { prevIssue, prevIssues } = updateIssueCaches(queryClient, identifier, (issue) => ({
        ...issue,
        orchestratorState: 'paused',
      }));
      const prevSnapshot = useItervoxStore.getState().snapshot;
      if (prevSnapshot) {
        const updated = optimisticPauseSnapshot(prevSnapshot, identifier);
        useItervoxStore.getState().patchSnapshot({
          running: updated.running,
          counts: updated.counts,
          paused: updated.paused,
        });
      }

      return {
        prevIssue,
        prevIssueIdentifier: identifier,
        prevIssues,
        prevSnapshot: prevSnapshot ?? undefined,
      };
    },
    mutationFn: async (identifier: string) => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/cancel`, {
        method: 'POST',
      });
      if (!res.ok) throw new Error(`cancelIssue failed: ${String(res.status)}`);
    },
    onError: makeRollbackHandler(queryClient),
    onSuccess: (_data, identifier) => {
      refreshIssueViews(queryClient, identifier);
    },
  });
}

export function useResumeIssue() {
  const queryClient = useQueryClient();
  return useMutation({
    onMutate: async (identifier: string) => {
      await queryClient.cancelQueries({ queryKey: ISSUES_KEY });
      await queryClient.cancelQueries({ queryKey: ISSUE_KEY(identifier) });
      const { prevIssue, prevIssues } = updateIssueCaches(queryClient, identifier, (issue) => ({
        ...issue,
        orchestratorState: 'running',
      }));
      const prevSnapshot = useItervoxStore.getState().snapshot;
      if (prevSnapshot) {
        const wasInPaused = prevSnapshot.paused.includes(identifier);
        const updatedPaused = prevSnapshot.paused.filter((id) => id !== identifier);
        // Use the issue's real tracker state for the badge — 'running' is an
        // orchestrator state, not a tracker state, so the badge would render
        // incorrectly until the next SSE update (FE-R10-2).
        const issueTrackerState = prevIssues?.find((i) => i.identifier === identifier)?.state ?? '';
        const optimisticRow = {
          identifier,
          state: issueTrackerState,
          turnCount: 0,
          tokens: 0,
          inputTokens: 0,
          outputTokens: 0,
          elapsedMs: 0,
          startedAt: new Date().toISOString(),
          sessionId: '',
        };
        useItervoxStore.getState().patchSnapshot({
          paused: updatedPaused,
          running: wasInPaused ? [...prevSnapshot.running, optimisticRow] : prevSnapshot.running,
          counts: {
            ...prevSnapshot.counts,
            paused: wasInPaused
              ? Math.max(0, prevSnapshot.counts.paused - 1)
              : prevSnapshot.counts.paused,
            running: wasInPaused ? prevSnapshot.counts.running + 1 : prevSnapshot.counts.running,
          },
        });
      }

      return {
        prevIssue,
        prevIssueIdentifier: identifier,
        prevIssues,
        prevSnapshot: prevSnapshot ?? undefined,
      };
    },
    mutationFn: async (identifier: string) => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/resume`, {
        method: 'POST',
      });
      if (!res.ok) throw new Error(`resumeIssue failed: ${String(res.status)}`);
    },
    onError: makeRollbackHandler(queryClient),
    onSuccess: (_data, identifier) => {
      refreshIssueViews(queryClient, identifier);
    },
  });
}

export function useTerminateIssue() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (identifier: string) => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/terminate`, {
        method: 'POST',
      });
      if (!res.ok) throw new Error(`terminateIssue failed: ${String(res.status)}`);
    },
    onSuccess: (_data, identifier) => {
      refreshIssueViews(queryClient, identifier);
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Terminate failed — please try again.');
    },
  });
}

export function useTriggerAIReview() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (identifier: string) => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/ai-review`, {
        method: 'POST',
      });
      if (!res.ok) throw new Error(`triggerAIReview failed: ${String(res.status)}`);
    },
    onSuccess: (_data, identifier) => {
      refreshIssueViews(queryClient, identifier);
    },
    onError: (err: unknown) => {
      toastApiError(err, 'AI review trigger failed — please try again.');
    },
  });
}

export function useClearIssueLogs() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (identifier: string) => {
      // "Clear logs" is a user-visible meta-action that must reset everything
      // the user sees on the Logs page for this issue:
      //   - the main Logs pane (backed by the in-memory logbuffer, wiped by
      //     DELETE /issues/{id}/logs)
      //   - the Timeline sub-agent panel (backed by per-session JSONL files,
      //     wiped by DELETE /issues/{id}/sublogs)
      // Until this change, only the first endpoint was hit — so the
      // Timeline kept showing the same data even after a successful clear.
      const encoded = encodeURIComponent(identifier);
      const [logsRes, subRes] = await Promise.all([
        authedFetch(`/api/v1/issues/${encoded}/logs`, { method: 'DELETE' }),
        authedFetch(`/api/v1/issues/${encoded}/sublogs`, { method: 'DELETE' }),
      ]);
      if (!logsRes.ok) throw new Error(`clearIssueLogs failed: ${String(logsRes.status)}`);
      if (!subRes.ok) throw new Error(`clearIssueSubLogs failed: ${String(subRes.status)}`);
    },
    onSuccess: (_data, identifier) => {
      void queryClient.invalidateQueries({ queryKey: logsKey(identifier) });
      void queryClient.invalidateQueries({ queryKey: sublogsKey(identifier) });
      void queryClient.invalidateQueries({ queryKey: logIdentifiersKey() });
      // refreshSnapshot pulls fresh history/running data that drives the
      // Timeline's run rows — without this, a cleared issue's run entries
      // linger in Timeline even though their underlying logs are gone.
      void useItervoxStore.getState().refreshSnapshot();
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Clear logs failed — please try again.');
    },
  });
}

export function useClearAllLogs() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const res = await authedFetch('/api/v1/logs', { method: 'DELETE' });
      if (!res.ok) throw new Error(`clearAllLogs failed: ${String(res.status)}`);
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['logs'] });
      void queryClient.invalidateQueries({ queryKey: ['sublogs'] });
      void queryClient.invalidateQueries({ queryKey: ['log-identifiers'] });
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Clear all logs failed — please try again.');
    },
  });
}

export function useClearAllWorkspaces() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const res = await authedFetch('/api/v1/workspaces', { method: 'DELETE' });
      if (!res.ok) throw new Error(`clearAllWorkspaces failed: ${String(res.status)}`);
    },
    onSuccess: () => {
      void useItervoxStore.getState().refreshSnapshot();
      void queryClient.invalidateQueries({ queryKey: ['logs'] });
      void queryClient.invalidateQueries({ queryKey: ['sublogs'] });
      void queryClient.invalidateQueries({ queryKey: logIdentifiersKey() });
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Reset workspaces failed — please try again.');
    },
  });
}

export function useClearIssueSubLogs() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (identifier: string) => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/sublogs`, {
        method: 'DELETE',
      });
      if (!res.ok) throw new Error(`clearIssueSubLogs failed: ${String(res.status)}`);
    },
    onSuccess: (_data, identifier) => {
      void queryClient.invalidateQueries({ queryKey: sublogsKey(identifier) });
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Clear session logs failed — please try again.');
    },
  });
}

export function useProvideInput() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ identifier, message }: { identifier: string; message: string }) => {
      const res = await authedFetch(
        `/api/v1/issues/${encodeURIComponent(identifier)}/provide-input`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ message }),
        },
      );
      if (!res.ok) throw new Error(`provideInput failed: ${String(res.status)}`);
    },
    onSuccess: (_data, { identifier }) => {
      refreshIssueViews(queryClient, identifier);
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Failed to send input to agent.');
    },
  });
}

export function useDismissInput() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (identifier: string) => {
      const res = await authedFetch(
        `/api/v1/issues/${encodeURIComponent(identifier)}/dismiss-input`,
        {
          method: 'POST',
        },
      );
      if (!res.ok) throw new Error(`dismissInput failed: ${String(res.status)}`);
    },
    onSuccess: (_data, identifier) => {
      refreshIssueViews(queryClient, identifier);
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Failed to dismiss input request.');
    },
  });
}

export function useReanalyzeIssue() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (identifier: string) => {
      const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/reanalyze`, {
        method: 'POST',
      });
      if (!res.ok) throw new Error(`reanalyzeIssue failed: ${String(res.status)}`);
    },
    onSuccess: (_data, identifier) => {
      refreshIssueViews(queryClient, identifier);
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Re-analysis failed — please try again.');
    },
  });
}
