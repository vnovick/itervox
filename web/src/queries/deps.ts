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

interface AnalyzeDepsInput {
  profile?: string;
}

const TERMINAL_STATUSES = new Set<DepsAnalyzeJob['status']>(['succeeded', 'failed']);

// Poll cadence: start at 1s, double until 5s, cap there. Chosen because the
// analyzer pass is typically 10–60s and we want to surface failure quickly
// without hammering the daemon for the common-case happy path.
const POLL_INTERVAL_START_MS = 1_000;
const POLL_INTERVAL_MAX_MS = 5_000;

async function pollUntilTerminal(jobId: string, signal: AbortSignal): Promise<DepsAnalyzeJob> {
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
    if (TERMINAL_STATUSES.has(job.status)) {
      return job;
    }
  }
  throw new Error('analyze deps timed out waiting for the job to finish');
}

export function useAnalyzeDeps() {
  return useMutation<DepsAnalyzeJob, Error, AnalyzeDepsInput>({
    mutationFn: async ({ profile }) => {
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
      const controller = new AbortController();
      const job = await pollUntilTerminal(enq.jobId, controller.signal);
      if (job.status === 'failed') {
        throw new Error(job.error || 'analyzer pass failed');
      }
      return job;
    },
    onSuccess: (job) => {
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
