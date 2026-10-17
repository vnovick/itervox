import { describe, it, expect, vi, beforeEach } from 'vitest';
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

vi.mock('../../../queries/trackerScope', () => ({
  useTrackerLabels: () => ({ data: ['agent', 'bug'] }),
  useTrackerUsers: () => ({ data: [{ id: 'u-ada', name: 'Ada Lovelace' }] }),
}));

vi.mock('../../../store/itervoxStore', () => ({
  useItervoxStore: vi.fn(),
}));

import { useItervoxStore } from '../../../store/itervoxStore';

const mockStore = useItervoxStore as unknown as ReturnType<typeof vi.fn>;

function withSnapshot(snapshot: Record<string, unknown>) {
  mockStore.mockImplementation((selector: (s: unknown) => unknown) => selector({ snapshot }));
}

function renderCard(activeFilter: string[] | undefined = []) {
  const onSetFilter = vi.fn(() => Promise.resolve(true));
  const onSetScope = vi.fn(() => Promise.resolve(true));
  render(
    <ProjectFilterCard
      activeFilter={activeFilter}
      onSetFilter={onSetFilter}
      onSetScope={onSetScope}
    />,
  );
  return { onSetFilter, onSetScope };
}

async function save(onSetScope: ReturnType<typeof vi.fn>, payload: unknown) {
  fireEvent.click(screen.getByRole('button', { name: 'Save filter' }));
  await vi.waitFor(() => {
    expect(onSetScope).toHaveBeenLastCalledWith(payload);
  });
}

describe('ProjectFilterCard — combined tracker scope (#182)', () => {
  beforeEach(() => {
    mockStore.mockReset();
    withSnapshot({ trackerScope: 'all projects' });
  });

  it('saves projects only', async () => {
    const { onSetScope } = renderCard();
    fireEvent.click(screen.getByRole('checkbox', { name: /Alpha/ }));
    await save(onSetScope, { projects: ['alpha'], assignee: '', labels: [] });
  });

  it('saves the assignee only (me)', async () => {
    const { onSetScope } = renderCard();
    fireEvent.change(screen.getByRole('combobox', { name: 'Assignee' }), {
      target: { value: 'me' },
    });
    await save(onSetScope, { projects: [], assignee: 'me', labels: [] });
  });

  it('saves labels only', async () => {
    const { onSetScope } = renderCard();
    fireEvent.click(screen.getByRole('checkbox', { name: 'bug' }));
    await save(onSetScope, { projects: [], assignee: '', labels: ['bug'] });
  });

  it('saves project AND a picked user AND labels', async () => {
    const { onSetScope } = renderCard();
    fireEvent.click(screen.getByRole('checkbox', { name: /Beta/ }));
    fireEvent.change(screen.getByRole('combobox', { name: 'Assignee' }), {
      target: { value: 'u-ada' },
    });
    fireEvent.click(screen.getByRole('checkbox', { name: 'agent' }));
    fireEvent.click(screen.getByRole('checkbox', { name: 'bug' }));
    await save(onSetScope, { projects: ['beta'], assignee: 'u-ada', labels: ['agent', 'bug'] });
  });

  it('starts from the current scope and clears it back to everything', async () => {
    withSnapshot({
      trackerScope: 'alpha · assigned to x · label: agent',
      scopeAssignee: 'ada@example.com',
      scopeLabels: ['agent'],
    });
    const { onSetScope } = renderCard(['alpha']);
    const select = screen.getByRole('combobox', { name: 'Assignee' });
    expect(select).toHaveValue('ada@example.com');
    expect(screen.getByRole('checkbox', { name: 'agent' })).toBeChecked();
    fireEvent.click(screen.getByRole('checkbox', { name: /All projects/ }));
    fireEvent.change(select, { target: { value: '' } });
    fireEvent.click(screen.getByRole('checkbox', { name: 'agent' }));
    await save(onSetScope, { projects: [], assignee: '', labels: [] });
  });

  it('keeps the projects-only filter for trackers without a combined scope', async () => {
    withSnapshot({});
    const { onSetFilter, onSetScope } = renderCard();
    expect(screen.queryByRole('combobox', { name: 'Assignee' })).toBeNull();
    fireEvent.click(screen.getByRole('checkbox', { name: /Alpha/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Save filter' }));
    await vi.waitFor(() => {
      expect(onSetFilter).toHaveBeenCalledWith(['alpha']);
    });
    expect(onSetScope).not.toHaveBeenCalled();
  });
});
