import type {
  RunningRow,
  RetryRow,
  HistoryRow,
  SSHHostInfo,
  ProfileDef,
  AutomationDef,
  InputRequiredEntry,
  DependencyGraphNode,
  DependencyGraphEdge,
  AutomationQueueRow,
  DependencyAuditRow,
  DependencyCycleRow,
  DependencyAttentionRow,
  OutboxEntryRow,
} from '../types/schemas';

/**
 * Stable empty-array constants for Zustand selector fallbacks.
 * Module-level constants prevent new references on every render,
 * which avoids useSyncExternalStore re-render loops.
 */

export const EMPTY_RUNNING: RunningRow[] = [];
export const EMPTY_RETRYING: RetryRow[] = [];
export const EMPTY_HISTORY: HistoryRow[] = [];
export const EMPTY_STATES: string[] = [];
export const EMPTY_HOSTS: SSHHostInfo[] = [];
export const EMPTY_PAUSED: string[] = [];
export const EMPTY_PROFILES: string[] = [];
export const EMPTY_PROFILE_DEFS: Record<string, ProfileDef> = {};
export const EMPTY_AUTOMATIONS: AutomationDef[] = [];
// v0.2.0 audit P3-7 — hoisted out of PendingResumePanel so the constant is
// declared before its first reference (no use-before-define hazard) and so
// other consumers can reuse the same stable reference instead of inlining.
export const EMPTY_INPUT_REQUIRED: InputRequiredEntry[] = [];

// v0.2.0 audit P2-9 — typed empties hoisted out of Dashboard/index.tsx so
// every empty fallback shares a single stable module-level reference,
// preventing useSyncExternalStore re-render loops and removing the three
// aliasing constants (EMPTY_BACKLOG_STATES / EMPTY_ACTIVE_STATES /
// EMPTY_TERMINAL_STATES) that just pointed at EMPTY_STATES.
export const EMPTY_DEPS_NODES: DependencyGraphNode[] = [];
export const EMPTY_DEPS_EDGES: DependencyGraphEdge[] = [];
export const EMPTY_AUTOMATION_QUEUE: AutomationQueueRow[] = [];
export const EMPTY_DEPENDENCY_AUDIT: DependencyAuditRow[] = [];
// critical-path-ordering Task 6 — stable empty fallback for the DepsGraph
// cycle-member edge highlight, following the same pattern as the constants
// above.
export const EMPTY_DEPENDENCY_CYCLES: DependencyCycleRow[] = [];
export const EMPTY_DEPENDENCY_ATTENTION: DependencyAttentionRow[] = [];
// outbox Task 4 — stable empty fallback for the Outbox panel.
export const EMPTY_OUTBOX_ENTRIES: OutboxEntryRow[] = [];
