import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { ProjectFilterCard } from '../ProjectFilterCard';

vi.mock('../../../queries/projects', () => ({
  useProjects: () => ({
    data: [
      { id: 'p1', name: 'Alpha', slug: 'alpha' },
      { id: 'p2', name: 'Beta', slug: 'beta' },
    ],
    isLoading: false,
    isError: false,
  }),
}));

function renderCard(activeFilter: string[] | undefined = []) {
  const onSetFilter = vi.fn(() => Promise.resolve(true));
  render(<ProjectFilterCard activeFilter={activeFilter} onSetFilter={onSetFilter} />);
  return { onSetFilter };
}

describe('ProjectFilterCard — projects only (#198 moved the scope to TrackerFilterCard)', () => {
  it('saves the checked projects', async () => {
    const { onSetFilter } = renderCard();
    expect(screen.queryByRole('combobox', { name: 'Assignee' })).toBeNull();
    fireEvent.click(screen.getByRole('checkbox', { name: /Alpha/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Save filter' }));
    await vi.waitFor(() => {
      expect(onSetFilter).toHaveBeenCalledWith(['alpha']);
    });
  });

  it('resets to the WORKFLOW.md default', async () => {
    const { onSetFilter } = renderCard(['beta']);
    fireEvent.click(screen.getByRole('button', { name: 'Reset to default' }));
    await vi.waitFor(() => {
      expect(onSetFilter).toHaveBeenCalledWith(null);
    });
  });
});
