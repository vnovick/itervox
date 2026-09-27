// CORE-080 — human labels for TrackerIssue.ineligibleReason, the machine
// reason internal/orchestrator/dispatch.go (ineligibleReasonShared and
// IneligibleReason) returns for an idle active-state issue, forwarded by
// internal/app/enrich.go. One table so cards, rows and the detail slide use
// the same words. Unknown reasons never throw: they render as the raw
// string, truncated.

export const INELIGIBLE_REASON_MAX_CHARS = 40;

// Prefixes mirror internal/orchestrator/dispatch_pressure.go.
const BLOCKED_BY_PREFIX = 'blocked_by:';
const INFERRED_BLOCKED_BY_PREFIX = 'inferred_blocked_by:';
const PAUSED_BY_STATE_PREFIX = 'paused_by_state:';

const EXACT: Record<string, string> = {
  no_slots: 'Waiting for a slot',
  per_state_limit: 'State capacity reached',
  claimed: 'Claimed by another agent',
  already_running: 'Running',
  discarding: 'Discarding',
  paused: 'Paused',
  input_required: 'Waiting for input',
  pending_input_resume: 'Waiting for input',
  // M3 / CORE-053: the dispatch gate held the issue because its backend
  // (and every backend_fallback target) is limited.
  backend_limited: 'Agent backend limited',
  missing_fields: 'Missing required fields',
  // Filtered out by enrich.go today; mapped so they stay readable if not.
  terminal_state: 'Not in an active state',
  not_active_state: 'Not in an active state',
};

function truncate(value: string): string {
  return value.length > INELIGIBLE_REASON_MAX_CHARS
    ? `${value.slice(0, INELIGIBLE_REASON_MAX_CHARS - 1)}…`
    : value;
}

export interface IneligibleReasonLabel {
  /** Short visible label. */
  label: string;
  /** Tooltip: the label plus the raw machine reason. */
  title: string;
}

export function ineligibleReasonLabel(reason: string): IneligibleReasonLabel {
  const label = labelFor(reason);
  return { label, title: reason ? `${label} (${reason})` : label };
}

function labelFor(reason: string): string {
  if (!reason) return 'Not dispatching';
  const exact = EXACT[reason];
  if (exact) return exact;
  if (reason.startsWith(BLOCKED_BY_PREFIX)) {
    const id = reason.slice(BLOCKED_BY_PREFIX.length);
    return id ? `Blocked by ${truncate(id)}` : 'Blocked by another issue';
  }
  if (reason.startsWith(INFERRED_BLOCKED_BY_PREFIX)) {
    const source = reason.slice(INFERRED_BLOCKED_BY_PREFIX.length);
    return source ? `Inferred block: ${truncate(source)}` : 'Inferred block';
  }
  if (reason.startsWith(PAUSED_BY_STATE_PREFIX)) {
    const state = reason.slice(PAUSED_BY_STATE_PREFIX.length);
    return state ? `Paused while ${truncate(state)} is busy` : 'Paused while another state is busy';
  }
  return truncate(reason);
}
