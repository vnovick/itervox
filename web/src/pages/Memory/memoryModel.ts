// Memory page helpers (#202): filtering and labels shared by its components.

import type { MemoryFilters } from '../../store/uiStore';
import type { MemoryEntry, MemoryScope, MemoryType } from '../../types/schemas';

export const TYPE_LABELS: Record<MemoryType, string> = {
  preference: 'Preference',
  fact: 'Fact',
  decision: 'Decision',
  lesson: 'Lesson',
};

export const SCOPE_INFO: Record<MemoryScope, { label: string; git: string }> = {
  private: { label: 'Private', git: 'Yours, in ~/.itervox/memory.json. Never committed.' },
  project: {
    label: 'Project shared',
    git: 'In .itervox/memory.json, committed with the code: the whole team’s runs use it.',
  },
};

const EMPTY_FILTER = '';

/**
 * The entries the Entries tab lists: everything but proposed lessons (those
 * wait in the Lessons tab), narrowed by the filters. Search matches text,
 * id, tags and the source issue, case-insensitively.
 */
export function filterEntries(entries: readonly MemoryEntry[], f: MemoryFilters): MemoryEntry[] {
  const q = f.search.trim().toLowerCase();
  return entries.filter((e) => {
    if (e.status === 'proposed') return false;
    if (f.type !== 'all' && e.type !== f.type) return false;
    if (f.scope !== 'all' && e.scope !== f.scope) return false;
    if (f.profile !== EMPTY_FILTER && e.profiles.length > 0 && !e.profiles.includes(f.profile)) {
      return false;
    }
    if (f.tag !== EMPTY_FILTER && !e.tags.includes(f.tag)) return false;
    if (f.usage === 'used' && e.useCount === 0) return false;
    if (f.usage === 'unused' && e.useCount > 0) return false;
    if (q === '') return true;
    return [e.text, e.id, e.source?.issue ?? '', ...e.tags].some((s) =>
      s.toLowerCase().includes(q),
    );
  });
}

/** Proposed entries: the Lessons tab's review queue (#203 fills its actions). */
export function proposedEntries(entries: readonly MemoryEntry[]): MemoryEntry[] {
  return entries.filter((e) => e.status === 'proposed');
}

/** Every distinct value of a list field across the entries, sorted. */
export function distinctValues(
  entries: readonly MemoryEntry[],
  field: 'tags' | 'profiles',
): string[] {
  return [...new Set(entries.flatMap((e) => e[field]))].sort();
}

/** "a, b" → ["a", "b"]; blanks dropped. */
export function splitList(value: string): string[] {
  return value
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s !== '');
}

/** The UTF-8 size of a text, as the daemon's cap counts it. */
export function byteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

/** #203 — what made Itervox propose a lesson, in words. */
export const TRIGGER_LABELS: Record<string, string> = {
  input_reply: 'Correction in an input reply',
  steering: 'Correction in a steering message',
  failed_then_succeeded: 'A failed run, then a successful one',
  reviewer_ask: 'A change reviewers asked for more than once',
  reverted_pr: 'A merged agent PR that was reverted',
  recipe: 'A hard-won success (recipe)',
};

/** #217 — enforcement kinds, strongest first, in words. */
export const ENFORCEMENT_KIND_LABELS: Record<string, string> = {
  lint: 'Lint rule',
  type: 'Type',
  test: 'Test',
  docs: 'CLAUDE.md / AGENTS.md rule',
  rule: 'Itervox rule',
};

/** #217 — approved lessons (the Lessons tab's "Approved" view). */
export function approvedLessons(entries: readonly MemoryEntry[]): MemoryEntry[] {
  return entries.filter((e) => e.type === 'lesson' && e.status === 'active');
}

/** #217 — "Not helping" labels. */
export const NOT_HELPING_LABELS: Record<string, string> = {
  unused: 'Unused',
  failing: 'Failing',
  enforced: 'Enforced',
};

/** #203 — approved lessons the daemon flags as not helping. */
export function notHelpingEntries(entries: readonly MemoryEntry[]): MemoryEntry[] {
  return entries.filter((e) => e.status === 'active' && e.notHelping !== undefined);
}

/** #203 — a 0..1 confidence as a percentage, "—" when unknown. */
export function confidenceLabel(confidence?: number): string {
  return confidence === undefined ? '—' : `${String(Math.round(confidence * 100))}%`;
}
