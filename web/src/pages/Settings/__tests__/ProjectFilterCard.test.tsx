import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { ProjectFilterCard, ASSIGNEE_SCOPE_FILTER } from '../ProjectFilterCard';

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

vi.mock('../../../store/itervoxStore', () => ({
  useItervoxStore: vi.fn(),
}));

import { useItervoxStore } from '../../../store/itervoxStore';

const mockStore = useItervoxStore as unknown as ReturnType<typeof vi.fn>;

function withAssignee(label: string | undefined) {
  mockStore.mockImplementation((selector: (s: unknown) => unknown) =>
    selector({ snapshot: { assigneeScopeLabel: label } }),
  );
}

describe('ProjectFilterCard — assignee scope (#182)', () => {
  beforeEach(() => {
    mockStore.mockReset();
  });

  it('offers "Assigned to …" and saves it alone, clearing projects', async () => {
    withAssignee('Itervox Bot');
    const onSetFilter = vi.fn(() => Promise.resolve(true));
    render(<ProjectFilterCard activeFilter={['alpha']} onSetFilter={onSetFilter} />);

    const assigned = screen.getByRole('checkbox', { name: /Assigned to Itervox Bot/ });
    expect(assigned).not.toBeChecked();
    fireEvent.click(assigned);
    expect(assigned).toBeChecked();
    expect(screen.getByRole('checkbox', { name: /Alpha/ })).not.toBeChecked();

    fireEvent.click(screen.getByRole('button', { name: 'Save filter' }));
    await vi.waitFor(() => {
      expect(onSetFilter).toHaveBeenCalledWith([ASSIGNEE_SCOPE_FILTER]);
    });
  });

  it('a project clears the assignee scope', () => {
    withAssignee('Itervox Bot');
    render(<ProjectFilterCard activeFilter={[ASSIGNEE_SCOPE_FILTER]} onSetFilter={vi.fn()} />);
    const assigned = screen.getByRole('checkbox', { name: /Assigned to Itervox Bot/ });
    expect(assigned).toBeChecked();
    fireEvent.click(screen.getByRole('checkbox', { name: /Beta/ }));
    expect(assigned).not.toBeChecked();
    expect(screen.getByRole('checkbox', { name: /Beta/ })).toBeChecked();
  });

  it('hides the option when the tracker has no assignee scope', () => {
    withAssignee(undefined);
    render(<ProjectFilterCard activeFilter={undefined} onSetFilter={vi.fn()} />);
    expect(screen.queryByRole('checkbox', { name: /Assigned to/ })).toBeNull();
  });
});
