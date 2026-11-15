import type { FilterGroup } from '../types/schemas';

// #198 — the tracker filter's conditions: a group ANDs the conditions it
// sets, a filter ORs its groups. Mirrors internal/issuefilter.

export type FilterConditionKey = keyof FilterGroup;

export type FilterConditionKind = 'list' | 'single' | 'priority';

export interface FilterConditionMeta {
  key: FilterConditionKey;
  label: string;
  kind: FilterConditionKind;
  hint: string;
}

export const FILTER_CONDITIONS: readonly FilterConditionMeta[] = [
  { key: 'projects', label: 'Projects', kind: 'list', hint: 'in any of these projects' },
  { key: 'teams', label: 'Teams', kind: 'list', hint: 'in any of these teams' },
  {
    key: 'assignee',
    label: 'Assignee',
    kind: 'single',
    hint: 'me, a user id, an email or a login',
  },
  { key: 'labels_any', label: 'Labels (any of)', kind: 'list', hint: 'has at least one' },
  { key: 'labels_all', label: 'Labels (all of)', kind: 'list', hint: 'has every one' },
  { key: 'labels_none', label: 'Labels (none of)', kind: 'list', hint: 'has none of them' },
  { key: 'priority', label: 'Priority', kind: 'priority', hint: 'any of these priorities' },
  { key: 'states', label: 'States', kind: 'list', hint: 'narrows the active states' },
  { key: 'cycle', label: 'Cycle', kind: 'single', hint: 'current, or a cycle id' },
  { key: 'milestone', label: 'Milestone', kind: 'single', hint: 'a milestone name' },
];

export const PRIORITY_LABELS: readonly string[] = ['None', 'Urgent', 'High', 'Normal', 'Low'];

export function conditionMeta(key: FilterConditionKey): FilterConditionMeta {
  return FILTER_CONDITIONS.find((c) => c.key === key) ?? FILTER_CONDITIONS[0];
}

/** The conditions a group sets, in display order. */
export function groupConditions(group: FilterGroup): FilterConditionKey[] {
  return FILTER_CONDITIONS.map((c) => c.key).filter((key) => {
    const value = group[key];
    return Array.isArray(value) ? true : value !== undefined;
  });
}

/** Whether a condition holds a value (an empty list or blank string is unset). */
function hasValue(value: FilterGroup[FilterConditionKey]): boolean {
  if (Array.isArray(value)) return value.length > 0;
  return typeof value === 'string' && value.trim() !== '';
}

/** Drops conditions without a value; a group left empty stays (and is invalid). */
export function cleanGroups(groups: readonly FilterGroup[]): FilterGroup[] {
  return groups.map((group) => {
    const out: FilterGroup = {};
    for (const key of groupConditions(group)) {
      const value = group[key];
      if (!hasValue(value)) continue;
      Object.assign(out, { [key]: typeof value === 'string' ? value.trim() : value });
    }
    return out;
  });
}

/** Index of the first group with no condition set, or -1. */
export function firstEmptyGroup(groups: readonly FilterGroup[]): number {
  return cleanGroups(groups).findIndex((g) => Object.keys(g).length === 0);
}

/** One group's summary, e.g. "ENG · agent|ai · ¬blocked" (as the server's). */
export function groupLabel(g: FilterGroup): string {
  const p: string[] = [];
  if (g.projects?.length)
    p.push(g.projects.map((s) => (s === '__no_project__' ? 'no project' : s)).join(','));
  if (g.teams?.length) p.push(`team ${g.teams.join(',')}`);
  if (g.assignee) p.push(g.assignee);
  if (g.labels_any?.length) p.push(g.labels_any.join('|'));
  if (g.labels_all?.length) p.push(g.labels_all.join('+'));
  for (const l of g.labels_none ?? []) p.push(`¬${l}`);
  if (g.priority?.length) p.push(g.priority.map((v) => `P${String(v)}`).join(','));
  if (g.states?.length) p.push(`state ${g.states.join(',')}`);
  if (g.cycle) p.push(`cycle ${g.cycle}`);
  if (g.milestone) p.push(`milestone ${g.milestone}`);
  return p.join(' · ');
}

/** The filter's summary: groups joined by "OR". */
export function filterLabel(groups: readonly FilterGroup[]): string {
  return cleanGroups(groups).map(groupLabel).join('  OR  ');
}
