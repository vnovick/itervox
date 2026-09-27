import { useMutation, useMutationState, useQuery, useQueryClient } from '@tanstack/react-query';
import type { QueryClient } from '@tanstack/react-query';
import { useItervoxStore } from '../store/itervoxStore';
import { useToastStore } from '../store/toastStore';
import type { StateSnapshot, TrackerIssue } from '../types/schemas';
import { TrackerIssueSchema } from '../types/schemas';
import { z } from 'zod';
import { apiRequest, ApiError } from '../auth/apiRequest';
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

// CORE-049: ApiError, the Retry-After helper and the single retry on an
// admission-rejected 503 (CORE-005) live in auth/apiRequest; re-exported here
// for existing importers.
export { ApiError, ISSUE_CONTROL_RETRY_MAX_MS, retryAfterDelayMs } from '../auth/apiRequest';

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

/**
 * CORE-073 — success toast for the issue control verbs (Pause, Resume, Stop,
 * Discard, Cancel retry). Worded "requested": the daemon only enqueues the
 * action on its event loop, so the toast must not claim it already happened.
 */
function toastActionRequested(verb: string, identifier: string): void {
  useToastStore.getState().addToast(`${verb} requested for ${identifier}`, 'success');
}

export interface IssueActionOptions {
  /** Operator-facing verb for the success toast. */
  verb?: string;
}

/**
 * Mutation keys for the per-issue actions a list can fire for several rows at
 * once (M5-close BH-M5-3). One useMutation instance tracks only its LATEST
 * call — isPending/variables and per-call mutate callbacks describe that call
 * alone — so a list reads every in-flight identifier from the mutation cache
 * via usePendingIssueIds instead.
 */
export const ISSUE_MUTATION_KEYS = {
  provideInput: ['issue-action', 'provideInput'],
  resume: ['issue-action', 'resume'],
  terminate: ['issue-action', 'terminate'],
  dismissInput: ['issue-action', 'dismissInput'],
} as const;

/** Identifiers with an in-flight mutation of `kind` (any hook instance). */
export function usePendingIssueIds(kind: keyof typeof ISSUE_MUTATION_KEYS): ReadonlySet<string> {
  const vars = useMutationState({
    filters: { mutationKey: ISSUE_MUTATION_KEYS[kind], status: 'pending' },
    select: (m) => m.state.variables,
  });
  const ids = new Set<string>();
  for (const v of vars) {
    if (typeof v === 'string') ids.add(v);
    else if (v && typeof v === 'object' && 'identifier' in v && typeof v.identifier === 'string') {
      ids.add(v.identifier);
    }
  }
  return ids;
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
  const { data } = await apiRequest('/api/v1/issues', { op: 'fetch issues' });
  return z.array(TrackerIssueSchema).parse(data);
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
      const { data } = await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}`, {
        op: 'fetch issue',
      });
      return TrackerIssueSchema.parse(data);
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
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/state`, {
        op: 'updateIssueState',
        method: 'PATCH',
        json: { state },
      });
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
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/profile`, {
        op: 'setIssueProfile',
        method: 'POST',
        json: { profile },
      });
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
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/backend`, {
        op: 'setIssueBackend',
        method: 'POST',
        json: { backend },
      });
    },
    onError: makeRollbackHandler(queryClient),
    onSuccess: (_data, { identifier }) => {
      invalidateIssueQueries(queryClient, identifier);
    },
  });
}

/** POST /cancel. Pauses a running issue; on a retrying issue it cancels the retry. */
export function useCancelIssue({ verb = 'Pause' }: IssueActionOptions = {}) {
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
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/cancel`, {
        op: 'cancelIssue',
        method: 'POST',
      });
    },
    onError: makeRollbackHandler(queryClient),
    onSuccess: (_data, identifier) => {
      toastActionRequested(verb, identifier);
      refreshIssueViews(queryClient, identifier);
    },
  });
}

export function useResumeIssue({ verb = 'Resume' }: IssueActionOptions = {}) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: ISSUE_MUTATION_KEYS.resume,
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
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/resume`, {
        op: 'resumeIssue',
        method: 'POST',
      });
    },
    onError: makeRollbackHandler(queryClient),
    onSuccess: (_data, identifier) => {
      toastActionRequested(verb, identifier);
      refreshIssueViews(queryClient, identifier);
    },
  });
}

/**
 * POST /terminate. The daemon releases the claim and moves the issue to the
 * first backlog state (else the first active state): "Stop" on a running row,
 * "Discard" on a paused one (TUI parity: S stop running, D discard paused).
 */
export function useTerminateIssue({ verb = 'Discard' }: IssueActionOptions = {}) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: ISSUE_MUTATION_KEYS.terminate,
    mutationFn: async (identifier: string) => {
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/terminate`, {
        op: 'terminateIssue',
        method: 'POST',
      });
    },
    onSuccess: (_data, identifier) => {
      toastActionRequested(verb, identifier);
      refreshIssueViews(queryClient, identifier);
    },
    onError: (err: unknown) => {
      toastApiError(err, `${verb} failed — please try again.`);
    },
  });
}

export function useTriggerAIReview() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (identifier: string) => {
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/ai-review`, {
        op: 'triggerAIReview',
        method: 'POST',
      });
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
      await Promise.all([
        apiRequest(`/api/v1/issues/${encoded}/logs`, { op: 'clearIssueLogs', method: 'DELETE' }),
        apiRequest(`/api/v1/issues/${encoded}/sublogs`, {
          op: 'clearIssueSubLogs',
          method: 'DELETE',
        }),
      ]);
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
      await apiRequest('/api/v1/logs', { op: 'clearAllLogs', method: 'DELETE' });
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
      await apiRequest('/api/v1/workspaces', { op: 'clearAllWorkspaces', method: 'DELETE' });
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
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/sublogs`, {
        op: 'clearIssueSubLogs',
        method: 'DELETE',
      });
    },
    onSuccess: (_data, identifier) => {
      void queryClient.invalidateQueries({ queryKey: sublogsKey(identifier) });
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Clear session logs failed — please try again.');
    },
  });
}

/**
 * Thrown by `useProvideInput`'s mutationFn on `409 inline_input_enabled` —
 * `agent.inline_input` was turned on (possibly from another tab) after this
 * issue's reply box was rendered, and the tracker is now the only reply
 * channel. Typed so `onError` can refresh the snapshot instead of leaving
 * the panel showing a stale reply box, and so the toast reads as an
 * operator-facing notice rather than the raw `provideInput failed: 409`
 * developer string.
 */
/** Server code for provide-input while agent.inline_input is on. */
const INLINE_INPUT_ENABLED_CODE = 'inline_input_enabled';

export class InlineInputEnabledError extends ApiError {
  constructor() {
    super(
      'Inline input is on — reply by commenting on this issue in your tracker.',
      409,
      INLINE_INPUT_ENABLED_CODE,
    );
    this.name = 'InlineInputEnabledError';
  }
}

export function useProvideInput({
  onSent,
}: {
  /**
   * Called from the hook-level onSuccess for EVERY successful reply, with its
   * identifier — unlike a per-call mutate callback, which fires only for the
   * latest call (M5-close BH-M5-3: a list clears each row's draft here).
   */
  onSent?: (identifier: string) => void;
} = {}) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: ISSUE_MUTATION_KEYS.provideInput,
    mutationFn: async ({ identifier, message }: { identifier: string; message: string }) => {
      try {
        await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/provide-input`, {
          op: 'provideInput',
          method: 'POST',
          json: { message },
        });
      } catch (err) {
        // CORE-049: keyed on the server's code, not the bare status — a 409
        // with any OTHER code surfaces its own message. A 409 with no
        // envelope (an older daemon) keeps the historical inline-input
        // reading.
        if (
          err instanceof ApiError &&
          err.status === 409 &&
          (err.code === INLINE_INPUT_ENABLED_CODE || err.code === undefined)
        ) {
          throw new InlineInputEnabledError();
        }
        throw err;
      }
    },
    onSuccess: (_data, { identifier }) => {
      onSent?.(identifier);
      refreshIssueViews(queryClient, identifier);
    },
    onError: (err: unknown) => {
      if (err instanceof InlineInputEnabledError) {
        // Another tab (or the operator) flipped agent.inline_input on since
        // this panel last saw a snapshot — refresh so the reply box is
        // replaced by the inline notice instead of staying stale.
        void useItervoxStore.getState().refreshSnapshot();
      }
      toastApiError(err, 'Failed to send input to agent.');
    },
  });
}

/**
 * Posts a plain operator comment on the issue via
 * `POST /api/v1/issues/{identifier}/comment`. Distinct from `useProvideInput`:
 * this is not an input-required reply — it behaves like a comment typed
 * directly in the tracker (can fire `tracker_comment_added` automations,
 * delivered through the write-ahead outbox when enabled). Returns
 * `{queued: true}` on `202` (outbox-accepted) or `{queued: false}` on `200`
 * (posted directly).
 */
export function usePostIssueComment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      identifier,
      body,
    }: {
      identifier: string;
      body: string;
    }): Promise<{ queued: boolean }> => {
      const { status } = await apiRequest(
        `/api/v1/issues/${encodeURIComponent(identifier)}/comment`,
        { op: 'postComment', method: 'POST', json: { body } },
      );
      return { queued: status === 202 };
    },
    onSuccess: ({ queued }, { identifier }) => {
      useToastStore
        .getState()
        .addToast(
          queued ? 'Comment queued — it will appear once delivered.' : 'Comment posted.',
          'success',
        );
      refreshIssueViews(queryClient, identifier);
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Failed to post comment.');
    },
  });
}

export function useDismissInput() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: ISSUE_MUTATION_KEYS.dismissInput,
    mutationFn: async (identifier: string) => {
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/dismiss-input`, {
        op: 'dismissInput',
        method: 'POST',
      });
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
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/reanalyze`, {
        op: 'reanalyzeIssue',
        method: 'POST',
      });
    },
    onSuccess: (_data, identifier) => {
      refreshIssueViews(queryClient, identifier);
    },
    onError: (err: unknown) => {
      toastApiError(err, 'Re-analysis failed — please try again.');
    },
  });
}

/**
 * CORE-175 — acknowledge an issue's failed / stalled attention entry. The
 * daemon records `upTo` (the newest failure the operator saw) on its event
 * loop and lists it in `snapshot.failureAcks`; a later failure surfaces
 * again. Optimistic: the entry leaves the inbox at once, and the previous
 * snapshot is restored (with an error toast) if the request fails. Callers
 * render the action only when `snapshot.capabilities` includes
 * 'failure_ack' (older daemons have no endpoint).
 */
export function useAcknowledgeFailure() {
  return useMutation({
    onMutate: ({ identifier, upTo }: { identifier: string; upTo: string }) => {
      const prevAcks = useItervoxStore.getState().snapshot?.failureAcks;
      if (useItervoxStore.getState().snapshot) {
        useItervoxStore.getState().patchSnapshot({
          failureAcks: [
            ...(prevAcks ?? []).filter((a) => a.identifier !== identifier),
            { identifier, upTo },
          ],
        });
      }
      return { prevAcks };
    },
    mutationFn: async ({ identifier, upTo }: { identifier: string; upTo: string }) => {
      await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/failures/ack`, {
        op: 'acknowledgeFailure',
        method: 'POST',
        json: { upTo },
      });
    },
    // M6-close — roll back only failureAcks: restoring the whole pre-click
    // snapshot would also discard any snapshot that arrived while the request
    // was in flight.
    onError: (error, _vars, context) => {
      if (useItervoxStore.getState().snapshot) {
        useItervoxStore.getState().patchSnapshot({ failureAcks: context?.prevAcks });
      }
      toastApiError(error);
    },
    onSuccess: (_data, { identifier }) => {
      useToastStore.getState().addToast(`Acknowledged the failure on ${identifier}`, 'success');
      void useItervoxStore.getState().refreshSnapshot();
    },
  });
}
