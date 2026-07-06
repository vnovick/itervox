export const AUTOMATION_TRIGGER_TYPES = [
  'cron',
  'input_required',
  'tracker_comment_added',
  'issue_entered_state',
  'issue_moved_to_backlog',
  'run_failed',
  'pr_opened',
  'pr_merged',
  'rate_limited',
  'blockers_resolved',
] as const;

export type AutomationTriggerType = (typeof AUTOMATION_TRIGGER_TYPES)[number];

export type AutomationTriggerMeta = {
  label: string;
  description: string;
};

export const AUTOMATION_TRIGGER_META = {
  cron: {
    label: 'Cron',
    description: 'Runs on a fixed schedule and dispatches matching issues in batches.',
  },
  input_required: {
    label: 'Input Required',
    description: 'Dispatches when a running agent blocks and asks for human input.',
  },
  tracker_comment_added: {
    label: 'Tracker Comment Added',
    description: 'Polls tracker comments and fires when Itervox sees a new latest comment.',
  },
  issue_entered_state: {
    label: 'Issue Entered State',
    description: 'Fires when an issue transitions into a specific tracker state.',
  },
  issue_moved_to_backlog: {
    label: 'Issue Moved To Backlog',
    description: 'Fires when an issue newly lands in one of the configured backlog states.',
  },
  run_failed: {
    label: 'Run Failed',
    description: 'Fires after a worker run fails permanently and Itervox stops retrying it.',
  },
  pr_opened: {
    label: 'PR Opened',
    description: 'Fires the moment a worker confirms a brand-new pull request for the issue.',
  },
  pr_merged: {
    label: 'PR Merged',
    description: 'Fires when a tracked pull request opened by an itervox-managed branch merges.',
  },
  rate_limited: {
    label: 'Rate Limited',
    description:
      'Fires when an exhausted-retry exit was caused by vendor rate-limit or quota errors.',
  },
  blockers_resolved: {
    label: 'Blockers Resolved',
    description: 'Fires when dependency audit observes a blocked issue becoming unblocked.',
  },
} satisfies Record<AutomationTriggerType, AutomationTriggerMeta>;

export const AUTOMATION_TRIGGER_OPTIONS = AUTOMATION_TRIGGER_TYPES.map((value) => ({
  value,
  ...AUTOMATION_TRIGGER_META[value],
}));

export type AutomationTriggerSummaryInput = {
  type: AutomationTriggerType;
  cron?: string;
  timezone?: string;
  state?: string;
};

export function automationTriggerSummary(trigger: AutomationTriggerSummaryInput): string {
  if (trigger.type === 'cron') {
    return trigger.timezone
      ? `${trigger.cron ?? 'Missing cron'} · ${trigger.timezone}`
      : (trigger.cron ?? 'Missing cron');
  }

  if (trigger.type === 'issue_entered_state' && trigger.state) {
    return `${AUTOMATION_TRIGGER_META[trigger.type].label} · ${trigger.state}`;
  }

  return AUTOMATION_TRIGGER_META[trigger.type].label;
}

/**
 * triggerTypeLabel returns the display label for a trigger type identifier.
 * Falls back to the raw type when the value is not a known
 * `AutomationTriggerType` (defensive against server-side additions the
 * client hasn't been updated for).
 *
 * v0.2.0 audit P2-8 — single source of truth for trigger-type labels.
 * Callers should use this helper instead of reimplementing the lookup.
 */
export function triggerTypeLabel(type: string): string {
  if (type in AUTOMATION_TRIGGER_META) {
    return AUTOMATION_TRIGGER_META[type as AutomationTriggerType].label;
  }
  return type;
}
