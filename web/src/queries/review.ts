// #238 — change review: an operator PR review (one GitHub review carrying
// the batched line comments; it never merges) and, for review and
// continue, the whole draft review sent to the agent. Operator scope. Every
// request goes through apiRequest → authedFetch. (The single-comment route
// POST /api/v1/issues/{identifier}/review-comments stays in the API; the
// dashboard now sends whole drafts.)
//
// POST /api/v1/issues/{identifier}/pr-review
// POST /api/v1/issues/{identifier}/review-continue (the whole draft)

import { useMutation, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';
import type { LineTarget } from '../store/reviewDraftStore';
import {
  PrReviewResultSchema,
  ReviewContinueResultSchema,
  type PrReviewResult,
  type ReviewContinueResult,
} from '../types/schemas';
import { RUN_CONTEXT_KEY } from './changes';

/** A PR review's event. */
export type PrReviewEvent = 'approve' | 'request_changes' | 'comment';

const enc = encodeURIComponent;

function toastFailure(prefix: string, err: unknown) {
  if (err instanceof UnauthorizedError) return; // AuthGate handles the UI
  const message = err instanceof Error ? err.message : 'unknown error';
  useToastStore.getState().addToast(`${prefix}: ${message}`, 'error');
}

/** Submits one PR review with the batched line comments. */
export function useSubmitPrReview(identifier: string) {
  return useMutation({
    mutationFn: async (vars: {
      event: PrReviewEvent;
      body: string;
      comments: readonly (LineTarget & { body: string })[];
    }): Promise<PrReviewResult> => {
      const { data } = await apiRequest(`/api/v1/issues/${enc(identifier)}/pr-review`, {
        op: 'PR review',
        method: 'POST',
        json: { event: vars.event, body: vars.body, comments: vars.comments },
      });
      return PrReviewResultSchema.parse(data);
    },
    onSuccess: () => {
      useToastStore.getState().addToast('Review submitted on GitHub', 'success');
    },
    onError: (err: unknown) => {
      toastFailure('Review not submitted', err);
    },
  });
}

/** The draft review sent to the agent in one action. */
export interface ReviewToAgent {
  comments: readonly (LineTarget & { body: string; quote?: string })[];
  note: string;
  /** Move an issue outside the active states to the first one first. */
  moveToActive: boolean;
  /** Also post the comments as one PR review. */
  alsoPostOnPR: boolean;
}

function agentToast(identifier: string, res: ReviewContinueResult, n: number): string {
  const what = `${String(n)} ${n === 1 ? 'comment' : 'comments'}`;
  if (res.agent.delivery === 'steered') return `Review sent to the running agent (${what})`;
  if (res.agent.movedTo) {
    return `Review queued (${what}); ${identifier} moved to ${res.agent.movedTo} and its run starts when admitted`;
  }
  return `Review queued (${what}); a run starts on ${identifier}'s branch when admitted`;
}

/**
 * Review and continue — POST /api/v1/issues/{identifier}/review-continue:
 * the whole draft to the agent. A live run gets it as steering; otherwise
 * it is queued for a new run on the issue's branch. With alsoPostOnPR the
 * comments also become one PR review; a PR failure is its own error toast
 * and never hides the agent outcome.
 */
export function useSendReviewToAgent(identifier: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (vars: ReviewToAgent): Promise<ReviewContinueResult> => {
      const { data } = await apiRequest(`/api/v1/issues/${enc(identifier)}/review-continue`, {
        op: 'review to agent',
        method: 'POST',
        json: {
          comments: vars.comments.map((c) => ({
            path: c.path,
            line: c.line,
            side: c.side,
            startLine: c.startLine,
            startSide: c.startSide,
            body: c.body,
            quote: c.quote,
          })),
          note: vars.note,
          moveToActive: vars.moveToActive,
          alsoPostOnPR: vars.alsoPostOnPR,
        },
      });
      return ReviewContinueResultSchema.parse(data);
    },
    onSuccess: (res, vars) => {
      const toast = useToastStore.getState().addToast;
      const pr = res.pr;
      toast(
        agentToast(identifier, res, vars.comments.length) +
          (pr?.posted ? ' · posted on the PR' : ''),
        'success',
      );
      if (pr && !pr.posted) {
        toast(`Not posted on the PR: ${pr.message ?? pr.error ?? 'unknown error'}`, 'error');
      }
      void queryClient.invalidateQueries({ queryKey: RUN_CONTEXT_KEY(identifier) });
    },
    onError: (err: unknown) => {
      toastFailure('Review not sent', err);
    },
  });
}
