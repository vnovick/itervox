import type { AutomationTriggerType } from '../../../types/automationTriggers';

// #173 PR-event filter fields: which trigger each belongs to, and the values
// GitHub reports for the enumerated ones (mirrors internal/config
// PRCheckConclusions / PRReviewStates / PRMergeableStates).
export const PR_EVENT_FILTER_FIELDS = [
  'checkNames',
  'conclusions',
  'reviewStates',
  'reviewAuthors',
  'mergeableStates',
] as const;

export type PREventFilterField = (typeof PR_EVENT_FILTER_FIELDS)[number];

export type PREventFilterValues = Record<PREventFilterField, string[]>;

export const PR_EVENT_FILTER_TRIGGER: Record<PREventFilterField, AutomationTriggerType> = {
  checkNames: 'pr_check_completed',
  conclusions: 'pr_check_completed',
  reviewStates: 'pr_review_submitted',
  reviewAuthors: 'pr_review_submitted',
  mergeableStates: 'pr_mergeable_changed',
};

export const PR_CHECK_CONCLUSIONS = [
  'success',
  'failure',
  'neutral',
  'cancelled',
  'skipped',
  'timed_out',
  'action_required',
  'stale',
  'error',
];
export const PR_REVIEW_STATES = ['approved', 'changes_requested', 'commented', 'dismissed'];
export const PR_MERGEABLE_STATES = [
  'clean',
  'dirty',
  'blocked',
  'behind',
  'unstable',
  'has_hooks',
  'draft',
];

/** The PR-event filters to persist for triggerType: only its own, only when set. */
export function prEventFilterFromValues(
  triggerType: AutomationTriggerType,
  values: PREventFilterValues,
): Partial<PREventFilterValues> {
  const out: Partial<PREventFilterValues> = {};
  for (const field of PR_EVENT_FILTER_FIELDS) {
    // Values restored from an older draft may lack the field.
    const chips = values[field] as string[] | undefined;
    if (PR_EVENT_FILTER_TRIGGER[field] === triggerType && chips && chips.length > 0) {
      out[field] = chips;
    }
  }
  return out;
}
