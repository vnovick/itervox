import { describe, expect, it } from 'vitest';
import { DEFAULT_MEMORY_FILTERS } from '../../../store/uiStore';
import { MemoryEntrySchema, type MemoryEntry } from '../../../types/schemas';
import {
  byteLength,
  distinctValues,
  filterEntries,
  proposedEntries,
  splitList,
} from '../memoryModel';

const T = '2026-10-01T12:00:00Z';
const e = (over: Record<string, unknown>): MemoryEntry =>
  MemoryEntrySchema.parse({
    id: 'm',
    type: 'fact',
    text: 't',
    scope: 'private',
    status: 'active',
    created: T,
    updated: T,
    path: '/p',
    ...over,
  });

describe('memoryModel (#202)', () => {
  const all = [
    e({ id: 'a', text: 'pnpm', tags: ['web'], useCount: 1 }),
    e({ id: 'b', text: 'reviewers only', profiles: ['reviewer'], scope: 'project' }),
    e({ id: 'c', text: 'draft', status: 'proposed' }),
  ];

  it('keeps proposed entries out of the list and in the lessons queue', () => {
    expect(filterEntries(all, DEFAULT_MEMORY_FILTERS).map((x) => x.id)).toEqual(['a', 'b']);
    expect(proposedEntries(all).map((x) => x.id)).toEqual(['c']);
  });

  it('treats an entry without profiles as matching every profile filter', () => {
    const f = { ...DEFAULT_MEMORY_FILTERS, profile: 'reviewer' };
    expect(filterEntries(all, f).map((x) => x.id)).toEqual(['a', 'b']);
    const g = { ...DEFAULT_MEMORY_FILTERS, profile: 'implementer' };
    expect(filterEntries(all, g).map((x) => x.id)).toEqual(['a']);
  });

  it('lists distinct tags and profiles, splits lists, and counts UTF-8 bytes', () => {
    expect(distinctValues(all, 'tags')).toEqual(['web']);
    expect(distinctValues(all, 'profiles')).toEqual(['reviewer']);
    expect(splitList(' a, ,b ')).toEqual(['a', 'b']);
    expect(byteLength('é')).toBe(2);
  });

  it('falls back on unknown enum values instead of failing the page', () => {
    const odd = e({ type: 'rumour', status: 'weird', scope: 'org' });
    expect([odd.type, odd.status, odd.scope]).toEqual(['fact', 'archived', 'private']);
  });
});
