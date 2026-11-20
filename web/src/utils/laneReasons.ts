import type { LaneReason } from '../types/schemas';

// guardrails.lanes — helpers over the daemon's lane reasons (LaneReason).

export const LANE_LABEL: Record<string, string> = {
  human: 'human',
  second_opinion: 'second opinion',
  auto: 'auto',
};

/** "path" or "path:line". */
export function laneReasonLocation(r: LaneReason): string {
  return r.line && r.line > 0 ? `${r.path}:${String(r.line)}` : r.path;
}

/**
 * How serious a finding looks: "high" (red) when it switches a check off or
 * hides its failure, "medium" (amber) for a skip a person may well accept.
 */
const HIGH_RULES = new Set([
  'ci_continue_on_error',
  'ci_or_true',
  'test_deleted',
  'git_no_verify',
  'coverage_lowered',
  'js_only',
]);

export function findingSeverity(r: LaneReason): 'high' | 'medium' {
  return HIGH_RULES.has(r.rule ?? '') ? 'high' : 'medium';
}

/** A stable key for one reason in a list. */
export function laneReasonKey(r: LaneReason): string {
  return `${r.kind}:${laneReasonLocation(r)}:${r.rule ?? ''}`;
}
