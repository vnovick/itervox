import { describe, expect, it } from 'vitest';
import { buildStateFilterPills } from '../stateFilterPills';

const base = {
  activeStates: ['In Progress'],
  backlogStates: ['Backlog'],
  terminalStates: ['Done'],
};

describe('buildStateFilterPills', () => {
  it('adds a pill for a completion state that is not terminal', () => {
    const pills = buildStateFilterPills({ ...base, completionState: 'In Review' });
    expect(pills.map((p) => p.label)).toEqual([
      'All Issues',
      'Active',
      'Backlog',
      'In Review',
      'Done',
    ]);
  });

  it('a terminal completion state folds into Done: one "Done" label, not two', () => {
    const pills = buildStateFilterPills({ ...base, completionState: 'Done' });
    const labels = pills.map((p) => p.label);
    expect(labels.filter((l) => l === 'Done')).toHaveLength(1);
    expect(labels).toEqual(['All Issues', 'Active', 'Backlog', 'Done']);
  });

  it('a completion state named "Done" that is not listed terminal still filters under Done', () => {
    const pills = buildStateFilterPills({
      ...base,
      terminalStates: ['Cancelled'],
      completionState: 'done',
    });
    expect(pills.find((p) => p.id === 'done')?.states).toEqual(['Cancelled', 'done']);
    expect(pills.some((p) => p.id === 'review')).toBe(false);
  });
});
