import { describe, it, expect } from 'vitest';
import { cleanGroups, filterLabel, firstEmptyGroup, groupConditions } from '../filterGroups';

// #198 — the builder's helpers mirror internal/issuefilter.

describe('filterGroups', () => {
  it('summarises a filter as the server does', () => {
    expect(
      filterLabel([
        { projects: ['ENG'], labels_any: ['agent', 'ai'], labels_none: ['blocked'] },
        { assignee: 'me', labels_all: ['urgent'], priority: [1, 2], cycle: 'current' },
      ]),
    ).toBe('ENG · agent|ai · ¬blocked  OR  me · urgent · P1,P2 · cycle current');
    expect(
      filterLabel([
        { projects: ['__no_project__'], teams: ['OPS'], states: ['Todo'], milestone: 'Q4' },
      ]),
    ).toBe('no project · team OPS · state Todo · milestone Q4');
  });

  it('drops conditions without a value and finds empty groups', () => {
    const groups = [{ labels_any: [], assignee: ' me ' }, { milestone: '' }];
    expect(cleanGroups(groups)).toEqual([{ assignee: 'me' }, {}]);
    expect(firstEmptyGroup(groups)).toBe(1);
    expect(firstEmptyGroup([{ priority: [0] }])).toBe(-1);
    expect(groupConditions({ milestone: '', labels_all: [] })).toEqual(['labels_all', 'milestone']);
  });
});
