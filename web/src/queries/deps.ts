/**
 * Phase 3.2 of v0.2.0 todolist6 — the `Analyze dependencies` mutation hook.
 *
 * The button submits `POST /api/v1/deps/analyze`, receives a job ID, and
 * polls `GET /api/v1/deps/analyze/:jobId` until the job reaches a terminal
 * status. On `succeeded` it triggers `refreshSnapshot()` so the inferred
 * edges appear immediately without waiting for the next SSE tick.
 *
 * SSE is type-less today (the broadcaster just signals "something changed"
 * and consumers pull the snapshot), so the deps service ALSO calls
 * `srv.Notify()` after writing the sidecar — which means most browsers will
 * see the new edges on the next snapshot push regardless of polling. The
 * polling path here is the belt-and-suspenders fallback that surfaces the
 * job's error message in a toast when the analyzer fails.
 */
import { useMutation } from '@tanstack/react-query';
import { authedFetch } from '../auth/authedFetch';
import { useItervoxStore } from '../store/itervoxStore';
import { useToastStore } from '../store/toastStore';
import {
  DepsAnalyzeEnqueueResponseSchema,
  DepsAnalyzeJobSchema,
  type DepsAnalyzeJob,
} from '../types/schemas';

// Surfaced to the caller as soon as the job ID is known (right after
// enqueue) and again on every poll tick, so a component (DepsGraph's
// toolbar) can show a Cancel control and live chunk progress without
// running a second, independent polling loop against the same endpoint.
// `status`/`chunksTotal`/`chunksDone` are absent on the enqueue-time call —
// only `jobId` is known at that point.
export interface DepsJobUpdate {
  jobId: string;
  status?: DepsAnalyzeJob['status'];
  chunksTotal?: number;
  chunksDone?: number;
  lastActivityAt?: string;
  // analyzer-autonomy Task 5 — 'manual' (operator click/API/CLI) vs 'auto'
  // (scheduler-initiated, Task 4). Absent on the enqueue-time onJobUpdate
  // call (only jobId is known then); populated from the first poll tick.
  trigger?: DepsAnalyzeJob['trigger'];
}

interface AnalyzeDepsInput {
  profile?: string;
  onJobUpdate?: (update: DepsJobUpdate) => void;
}

const TERMINAL_STATUSES = new Set<DepsAnalyzeJob['status']>(['succeeded', 'failed', 'cancelled']);

// Poll cadence: start at 1s, double until 5s, cap there. Chosen because the
// analyzer pass is typically 10–60s and we want to surface failure quickly
// without hammering the daemon for the common-case happy path.
const POLL_INTERVAL_START_MS = 1_000;
const POLL_INTERVAL_MAX_MS = 5_000;

async function pollUntilTerminal(
  jobId: string,
  signal: AbortSignal,
  onJobUpdate?: (update: DepsJobUpdate) => void,
): Promise<DepsAnalyzeJob> {
  let delay = POLL_INTERVAL_START_MS;
  // Cap total polling at 10 minutes — analyzer is bounded by the daemon's
  // turn_timeout_ms, but we add a fallback so a stuck client doesn't poll
  // forever.
  const deadline = Date.now() + 10 * 60 * 1000;
  while (Date.now() < deadline) {
    if (signal.aborted) {
      throw new Error('analyze deps poll aborted');
    }
    await new Promise((resolve) => setTimeout(resolve, delay));
    delay = Math.min(delay * 2, POLL_INTERVAL_MAX_MS);
    const res = await authedFetch(`/api/v1/deps/analyze/${encodeURIComponent(jobId)}`);
    if (!res.ok) {
      // 404 means the job vanished — surface the failure and stop polling.
      throw new Error(`analyze deps status check failed (${String(res.status)})`);
    }
    const json: unknown = await res.json();
    const job = DepsAnalyzeJobSchema.parse(json);
    onJobUpdate?.({
      jobId: job.jobId,
      status: job.status,
      chunksTotal: job.chunksTotal,
      chunksDone: job.chunksDone,
      lastActivityAt: job.lastActivityAt,
      trigger: job.trigger,
    });
    if (TERMINAL_STATUSES.has(job.status)) {
      return job;
    }
  }
  throw new Error('analyze deps timed out waiting for the job to finish');
}

export function useAnalyzeDeps() {
  return useMutation<DepsAnalyzeJob, Error, AnalyzeDepsInput>({
    mutationFn: async ({ profile, onJobUpdate }) => {
      const res = await authedFetch('/api/v1/deps/analyze', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(profile ? { profile } : {}),
      });
      if (!res.ok) {
        const text = await res.text().catch(() => '');
        throw new Error(text || `analyze deps failed (${String(res.status)})`);
      }
      const json: unknown = await res.json();
      const enq = DepsAnalyzeEnqueueResponseSchema.parse(json);
      onJobUpdate?.({ jobId: enq.jobId });
      const controller = new AbortController();
      const job = await pollUntilTerminal(enq.jobId, controller.signal, onJobUpdate);
      if (job.status === 'failed') {
        throw new Error(job.error || 'analyzer pass failed');
      }
      return job;
    },
    onSuccess: (job) => {
      if (job.status === 'cancelled') {
        useToastStore.getState().addToast('Dependency analysis cancelled.', 'info');
        return;
      }
      useToastStore
        .getState()
        .addToast(
          `Analyzed dependencies — ${String(job.edgesFound ?? 0)} inferred edge(s).`,
          'success',
        );
      void useItervoxStore.getState().refreshSnapshot();
    },
    onError: (err) => {
      const message = err instanceof Error ? err.message : 'Unknown error';
      useToastStore.getState().addToast(`Analyze dependencies failed: ${message}`, 'error');
    },
  });
}

export function useCancelAnalyzeDeps() {
  return useMutation<undefined, Error, { jobId: string }>({
    mutationFn: async ({ jobId }) => {
      const res = await authedFetch(`/api/v1/deps/analyze/${encodeURIComponent(jobId)}`, {
        method: 'DELETE',
      });
      // 404 means the job already finished between the click and the request
      // landing — a normal race, not an error worth surfacing.
      if (!res.ok && res.status !== 404) {
        throw new Error(`cancel failed (${String(res.status)})`);
      }
    },
    onError: (err) => {
      const message = err instanceof Error ? err.message : 'Unknown error';
      useToastStore.getState().addToast(`Cancel failed: ${message}`, 'error');
    },
  });
}

export interface SetDepsOverrideInput {
  identifier: string;
  // true dismisses the issue's inferred blockers (POST), false restores them
  // (DELETE). Both requests are queued through the orchestrator's event loop
  // and answer 202 Accepted — the resulting InferredDeps/edge state lands on
  // the next snapshot, which is why the mutation always calls
  // refreshSnapshot() on success rather than trusting the 202 body.
  enabled: boolean;
}

/**
 * unified-dependency-graph Task 8 — dismiss/restore the LLM-inferred
 * dependency gating layer for one issue via
 * POST/DELETE /api/v1/issues/{identifier}/deps-override (Task 6 backend).
 * Mirrors the useAnalyzeDeps/useCancelAnalyzeDeps shape in this file:
 * authedFetch for transport, refreshSnapshot() on success (never
 * patchSnapshot — the override changes server-computed gating state that
 * only the daemon can recompute), toast on failure.
 */
export function useSetDepsOverride() {
  return useMutation<SetDepsOverrideInput, Error, SetDepsOverrideInput>({
    mutationFn: async ({ identifier, enabled }) => {
      const res = await authedFetch(
        `/api/v1/issues/${encodeURIComponent(identifier)}/deps-override`,
        { method: enabled ? 'POST' : 'DELETE' },
      );
      if (!res.ok) {
        const text = await res.text().catch(() => '');
        throw new Error(text || `dependency override failed (${String(res.status)})`);
      }
      return { identifier, enabled };
    },
    onSuccess: ({ identifier, enabled }) => {
      useToastStore
        .getState()
        .addToast(
          enabled
            ? `Dismissed inferred blockers for ${identifier}.`
            : `Restored inferred blockers for ${identifier}.`,
          'success',
        );
      void useItervoxStore.getState().refreshSnapshot();
    },
    onError: (err) => {
      const message = err instanceof Error ? err.message : 'Unknown error';
      useToastStore.getState().addToast(`Dependency override failed: ${message}`, 'error');
    },
  });
}
