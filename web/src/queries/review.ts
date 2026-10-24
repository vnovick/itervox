// #238 — change review: a line comment sent to the running agent or posted
// on the issue's PR, and an operator PR review (one GitHub review carrying
// the batched line comments; it never merges). Operator scope. Every
// request goes through apiRequest → authedFetch.
//
// POST /api/v1/issues/{identifier}/review-comments
// POST /api/v1/issues/{identifier}/pr-review

import { useMutation, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';
import type { LineTarget } from '../store/reviewDraftStore';
import {
  PrReviewResultSchema,
  ReviewCommentResultSchema,
  type PrReviewResult,
  type ReviewCommentResult,
} from '../types/schemas';
import { RUN_CONTEXT_KEY } from './changes';

/** Where a line comment goes. */
export type CommentDestination = 'agent' | 'pr';

/** A PR review's event. */
export type PrReviewEvent = 'approve' | 'request_changes' | 'comment';

const enc = encodeURIComponent;

function toastFailure(prefix: string, err: unknown) {
  if (err instanceof UnauthorizedError) return; // AuthGate handles the UI
  const message = err instanceof Error ? err.message : 'unknown error';
  useToastStore.getState().addToast(`${prefix}: ${message}`, 'error');
}

/** Sends one line comment to the running agent or posts it on the PR. */
export function useSendLineComment(identifier: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (vars: {
      destination: CommentDestination;
      target: LineTarget;
      body: string;
      quote?: string;
    }): Promise<ReviewCommentResult> => {
      const { data } = await apiRequest(`/api/v1/issues/${enc(identifier)}/review-comments`, {
        op: 'review comment',
        method: 'POST',
        json: {
          destination: vars.destination,
          ...vars.target,
          body: vars.body,
          quote: vars.destination === 'agent' ? vars.quote : undefined,
        },
      });
      return ReviewCommentResultSchema.parse(data);
    },
    onSuccess: (res) => {
      useToastStore
        .getState()
        .addToast(
          res.destination === 'agent' ? 'Comment sent to the agent' : 'Comment posted on the PR',
          'success',
        );
      if (res.destination === 'agent') {
        void queryClient.invalidateQueries({ queryKey: RUN_CONTEXT_KEY(identifier) });
      }
    },
    onError: (err: unknown) => {
      toastFailure('Comment not sent', err);
    },
  });
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
