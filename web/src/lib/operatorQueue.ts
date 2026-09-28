// Pure derivation helper for the attention inbox (CORE-077), the
// Notifications dashboard tab, the nav badge and the page-title count
// (CORE-078). No React imports. Output shape is `OperatorQueueItem[]`,
// grouped by the operator-attention categories defined below.
//
// `attention` is THE attention count: every item except the read-only
// `resuming` rows. The inbox heading, nav badge and title all display it.

import type { FailureRow, StateSnapshot, TrackerIssue } from '../types/schemas';
import { inputRequiredRowState } from '../utils/inputRequired';
import { failureKindLabel } from '../pages/Dashboard/components/failuresPanelModel';
import { STATUS_META } from './statusModel';

export type OperatorQueueGroup =
  | 'needs_input'
  | 'resuming'
  | 'failed'
  | 'review'
  | 'retrying'
  | 'paused'
  | 'backend_limited'
  | 'outbox'
  | 'config';

export type OperatorQueueClickAction =
  | { type: 'select-issue'; identifier: string }
  | { type: 'navigate'; path: string }
  | { type: 'none' };

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
  // CORE-175 — failed-group items: the newest failure's time (the ack `upTo`).
  occurredAt?: string;
}

export interface OperatorQueueGroupResult {
  group: OperatorQueueGroup;
  label: string;
  items: OperatorQueueItem[];
}

export interface OperatorQueueResult {
  groups: OperatorQueueGroupResult[];
  /** Every rendered item, read-only resuming rows included. */
  total: number;
  /** Items that need an operator: total minus the read-only resuming rows. */
  attention: number;
}

/** Groups shown for information only; never counted as attention. */
export const READ_ONLY_GROUPS: ReadonlySet<OperatorQueueGroup> = new Set(['resuming']);

const GROUP_LABEL: Record<OperatorQueueGroup, string> = {
  needs_input: STATUS_META.input_required.label,
  resuming: STATUS_META.pending_input_resume.label,
  failed: 'Failed or stalled',
  review: 'Ready for review',
  retrying: STATUS_META.retrying.label,
  paused: STATUS_META.paused.label,
  backend_limited: 'Agent backend limited',
  outbox: 'Tracker writes failing',
  config: 'Config issues',
};

/** Worker failures that belong to one issue and warrant a look. */
const ISSUE_FAILURE_KINDS: ReadonlySet<string> = new Set(['worker_failed', 'worker_stalled']);

/** The snapshot branches the queue reads (useOperatorQueue selects exactly these). */
export type OperatorQueueInput = Pick<
  StateSnapshot,
  | 'backendHealth'
  | 'completionState'
  | 'configInvalid'
  | 'currentAppSessionId'
  | 'history'
  | 'inputRequired'
  | 'outboxEntries'
  | 'paused'
  | 'pausedWithPR'
  | 'recentFailures'
  | 'failureAcks'
  | 'retrying'
  | 'running'
>;

export function buildOperatorQueueItems(
  snapshot: OperatorQueueInput,
  issues: readonly TrackerIssue[],
): OperatorQueueResult {
  const issuesByIdentifier = new Map(issues.map((i) => [i.identifier, i]));
  const groups: OperatorQueueGroupResult[] = [];

  // 1. Needs input (input_required) and 1b. Resuming (pending_input_resume,
  // read-only). CORE-076: split through inputRequiredRowState so the counts
  // match summarizeStatus and rows without `state` keep the fallback.
  const needsInputItems: OperatorQueueItem[] = [];
  const resumingItems: OperatorQueueItem[] = [];
  for (const entry of snapshot.inputRequired ?? []) {
    const resuming = inputRequiredRowState(entry) === 'pending_input_resume';
    (resuming ? resumingItems : needsInputItems).push({
      id: `${resuming ? 'resuming' : 'input'}:${entry.identifier}`,
      group: resuming ? 'resuming' : 'needs_input',
      identifier: entry.identifier,
      title: entry.identifier,
      subtitle: entry.context || undefined,
      meta: resuming ? 'reply pending — resuming' : 'awaiting reply',
      tone: resuming ? 'info' : 'warning',
      clickAction: { type: 'select-issue', identifier: entry.identifier },
    });
  }
  if (needsInputItems.length > 0) {
    groups.push({ group: 'needs_input', label: GROUP_LABEL.needs_input, items: needsInputItems });
  }
  if (resumingItems.length > 0) {
    groups.push({ group: 'resuming', label: GROUP_LABEL.resuming, items: resumingItems });
  }

  // 1c. Failed or stalled workers (CORE-077) — from the RecentFailures ring.
  const failedItems = failedWorkerItems(snapshot, issuesByIdentifier);
  if (failedItems.length > 0) {
    groups.push({ group: 'failed', label: GROUP_LABEL.failed, items: failedItems });
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

  // 5. Agent backends held by a limit (CORE-055 backendHealth) — one row
  // per backend/host. Nothing to open: the Live-ops chip carries the Clear
  // action, so the row is informational (clickAction none).
  const backendItems: OperatorQueueItem[] = [];
  for (const row of snapshot.backendHealth ?? []) {
    if (row.status !== 'limited') continue;
    const key = `${row.backend}@${row.host ?? ''}`;
    const name = row.host ? `${row.backend}@${row.host}` : row.backend;
    backendItems.push({
      id: `backend:${key}`,
      group: 'backend_limited',
      title: name,
      subtitle: row.heldIssues ? `${String(row.heldIssues)} issues held` : undefined,
      meta: row.limitedUntil
        ? `limited until ${new Date(row.limitedUntil).toLocaleString()}`
        : 'limited (reset unknown)',
      tone: 'danger',
      clickAction: { type: 'none' },
    });
  }
  if (backendItems.length > 0) {
    groups.push({
      group: 'backend_limited',
      label: GROUP_LABEL.backend_limited,
      items: backendItems,
    });
  }

  // 6. Degraded outbox entries — tracker writes that keep failing.
  const outboxItems: OperatorQueueItem[] = [];
  for (const entry of snapshot.outboxEntries ?? []) {
    if (!entry.degraded) continue;
    outboxItems.push({
      id: `outbox:${entry.id}`,
      group: 'outbox',
      identifier: entry.identifier,
      title: entry.identifier,
      subtitle: entry.lastError || undefined,
      meta: `${entry.kind} · ${String(entry.attempts)} attempts`,
      tone: 'danger',
      clickAction: { type: 'select-issue', identifier: entry.identifier },
    });
  }
  if (outboxItems.length > 0) {
    groups.push({ group: 'outbox', label: GROUP_LABEL.outbox, items: outboxItems });
  }

  // 7. Config issues — single row when configInvalid is present.
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
  const attention = groups.reduce(
    (sum, g) => (READ_ONLY_GROUPS.has(g.group) ? sum : sum + g.items.length),
    0,
  );
  return { groups, total, attention };
}

// failedWorkerItems: worker_failed / worker_stalled failures, one row per
// issue (its newest failure), newest first. Skipped when the issue is
// already being handled elsewhere in the queue or by the daemon — running,
// retrying, or waiting on input — or when a later run of it succeeded. A
// paused issue keeps its row: it then appears in both groups, each with its
// own actions.
function failedWorkerItems(
  snapshot: OperatorQueueInput,
  issuesByIdentifier: ReadonlyMap<string, TrackerIssue>,
): OperatorQueueItem[] {
  const handled = new Set<string>([
    ...snapshot.running.map((r) => r.identifier),
    ...snapshot.retrying.map((r) => r.identifier),
    ...(snapshot.inputRequired ?? []).map((r) => r.identifier),
  ]);
  const latest = new Map<string, FailureRow>();
  for (const f of snapshot.recentFailures ?? []) {
    if (!f.identifier || !ISSUE_FAILURE_KINDS.has(f.kind) || handled.has(f.identifier)) continue;
    const prev = latest.get(f.identifier);
    if (!prev || Date.parse(f.occurredAt) > Date.parse(prev.occurredAt)) {
      latest.set(f.identifier, f);
    }
  }
  // CORE-175 — an operator acknowledgement hides failures up to its time; a
  // later failure of the same issue surfaces again.
  const ackedUpTo = new Map<string, number>();
  for (const a of snapshot.failureAcks ?? []) {
    ackedUpTo.set(
      a.identifier,
      Math.max(Date.parse(a.upTo), ackedUpTo.get(a.identifier) ?? -Infinity),
    );
  }
  for (const [id, f] of latest) {
    if (Date.parse(f.occurredAt) <= (ackedUpTo.get(id) ?? -Infinity)) latest.delete(id);
  }
  const lastSuccess = new Map<string, number>();
  for (const h of snapshot.history ?? []) {
    if (h.status !== 'succeeded') continue;
    const at = Date.parse(h.finishedAt);
    if (at > (lastSuccess.get(h.identifier) ?? -Infinity)) lastSuccess.set(h.identifier, at);
  }
  return [...latest.values()]
    .filter((f) => !(Date.parse(f.occurredAt) < (lastSuccess.get(f.identifier ?? '') ?? -Infinity)))
    .sort((a, b) => Date.parse(b.occurredAt) - Date.parse(a.occurredAt))
    .map((f) => {
      const identifier = f.identifier ?? '';
      return {
        id: `failed:${identifier}`,
        group: 'failed' as const,
        identifier,
        title: identifier,
        subtitle: issuesByIdentifier.get(identifier)?.title ?? f.message,
        meta: failureKindLabel(f.kind),
        occurredAt: f.occurredAt,
        tone: 'danger' as const,
        clickAction: { type: 'select-issue' as const, identifier },
      };
    });
}

// Exported so `ReviewQueueSection` can share the classification logic
// without duplicating the loop. Gap §10.1.
export function classifyReviewSource(
  snapshot: Pick<StateSnapshot, 'currentAppSessionId' | 'history'>,
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
