// Pure derivation helper for the Notifications dashboard tab. No React
// imports — `NotificationsView` calls this with snapshot + issues and
// renders the result. Output shape is `OperatorQueueItem[]`, grouped by
// the five operator-attention categories defined below.

import type { StateSnapshot, TrackerIssue } from '../types/schemas';

export type OperatorQueueGroup = 'needs_input' | 'review' | 'retrying' | 'paused' | 'config';

export type OperatorQueueClickAction =
  | { type: 'select-issue'; identifier: string }
  | { type: 'navigate'; path: string };

export interface OperatorQueueItem {
  id: string;
  group: OperatorQueueGroup;
  identifier?: string;
  title: string;
  subtitle?: string;
  meta?: string;
  tone: 'info' | 'warning' | 'danger';
  clickAction: OperatorQueueClickAction;
  // Only populated for review-group items.
  reviewSource?: 'session' | 'tracker';
}

export interface OperatorQueueGroupResult {
  group: OperatorQueueGroup;
  label: string;
  items: OperatorQueueItem[];
}

export interface OperatorQueueResult {
  groups: OperatorQueueGroupResult[];
  total: number;
}

const GROUP_LABEL: Record<OperatorQueueGroup, string> = {
  needs_input: 'Needs input',
  review: 'Ready for review',
  retrying: 'Retrying',
  paused: 'Paused',
  config: 'Config issues',
};

export function buildOperatorQueueItems(
  snapshot: StateSnapshot,
  issues: readonly TrackerIssue[],
): OperatorQueueResult {
  const issuesByIdentifier = new Map(issues.map((i) => [i.identifier, i]));
  const groups: OperatorQueueGroupResult[] = [];

  // 1. Needs input — both input_required AND pending_input_resume.
  const needsInputItems: OperatorQueueItem[] = [];
  for (const entry of snapshot.inputRequired ?? []) {
    needsInputItems.push({
      id: `input:${entry.identifier}`,
      group: 'needs_input',
      identifier: entry.identifier,
      title: entry.identifier,
      subtitle: entry.context || undefined,
      meta: entry.state === 'pending_input_resume' ? 'reply pending — resuming' : 'awaiting reply',
      tone: 'warning',
      clickAction: { type: 'select-issue', identifier: entry.identifier },
    });
  }
  if (needsInputItems.length > 0) {
    groups.push({ group: 'needs_input', label: GROUP_LABEL.needs_input, items: needsInputItems });
  }

  // 2. Ready for review — issues in completionState with no live reviewer.
  // Mirrors ReviewQueueSection.tsx:30-40 logic.
  const reviewItems: OperatorQueueItem[] = [];
  const completionState = snapshot.completionState?.toLowerCase() ?? '';
  if (completionState) {
    const liveReviewerIdentifiers = new Set(
      snapshot.running.filter((r) => r.kind === 'reviewer').map((r) => r.identifier),
    );
    for (const issue of issues) {
      if (issue.state.toLowerCase() !== completionState) continue;
      if (liveReviewerIdentifiers.has(issue.identifier)) continue;
      const reviewSource = inferReviewSource(snapshot, issue.identifier);
      reviewItems.push({
        id: `review:${issue.identifier}`,
        group: 'review',
        identifier: issue.identifier,
        title: issue.identifier,
        subtitle: issue.title,
        meta: reviewSource === 'session' ? 'completed this session' : 'in review (tracker)',
        tone: 'info',
        clickAction: { type: 'select-issue', identifier: issue.identifier },
        reviewSource,
      });
    }
  }
  if (reviewItems.length > 0) {
    groups.push({ group: 'review', label: GROUP_LABEL.review, items: reviewItems });
  }

  // 3. Retrying.
  const retryItems: OperatorQueueItem[] = [];
  for (const r of snapshot.retrying) {
    const issue = issuesByIdentifier.get(r.identifier);
    retryItems.push({
      id: `retry:${r.identifier}`,
      group: 'retrying',
      identifier: r.identifier,
      title: r.identifier,
      subtitle: issue?.title,
      meta: `attempt ${String(r.attempt)}`,
      tone: 'warning',
      clickAction: { type: 'select-issue', identifier: r.identifier },
    });
  }
  if (retryItems.length > 0) {
    groups.push({ group: 'retrying', label: GROUP_LABEL.retrying, items: retryItems });
  }

  // 4. Paused — with optional pausedWithPR url.
  const pausedItems: OperatorQueueItem[] = [];
  for (const id of snapshot.paused) {
    const prURL = snapshot.pausedWithPR?.[id];
    const issue = issuesByIdentifier.get(id);
    pausedItems.push({
      id: `paused:${id}`,
      group: 'paused',
      identifier: id,
      title: id,
      subtitle: issue?.title,
      meta: prURL ?? 'paused',
      tone: 'info',
      clickAction: { type: 'select-issue', identifier: id },
    });
  }
  if (pausedItems.length > 0) {
    groups.push({ group: 'paused', label: GROUP_LABEL.paused, items: pausedItems });
  }

  // 5. Config issues — single row when configInvalid is present.
  if (snapshot.configInvalid) {
    groups.push({
      group: 'config',
      label: GROUP_LABEL.config,
      items: [
        {
          id: 'config:invalid',
          group: 'config',
          title: 'WORKFLOW.md validation failed',
          subtitle: snapshot.configInvalid.error,
          meta: snapshot.configInvalid.path,
          tone: 'danger',
          clickAction: { type: 'navigate', path: '/settings' },
        },
      ],
    });
  }

  const total = groups.reduce((sum, g) => sum + g.items.length, 0);
  return { groups, total };
}

// Exported so `ReviewQueueSection` can share the classification logic
// without duplicating the loop. Gap §10.1.
export function classifyReviewSource(
  snapshot: StateSnapshot,
  identifier: string,
): 'session' | 'tracker' {
  const sessionId = snapshot.currentAppSessionId;
  if (!sessionId) return 'tracker';
  for (const h of snapshot.history ?? []) {
    if (h.identifier !== identifier) continue;
    if (h.kind !== 'worker') continue;
    if (h.status !== 'succeeded') continue;
    if (h.appSessionId === sessionId) return 'session';
  }
  return 'tracker';
}

// Internal alias kept so the existing call site in buildOperatorQueueItems
// continues to read naturally.
const inferReviewSource = classifyReviewSource;
