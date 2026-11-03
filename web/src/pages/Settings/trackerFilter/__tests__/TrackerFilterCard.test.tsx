// #198 — Settings → Tracker Scope: the tracker filter group builder.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { TrackerFilterCard } from '../TrackerFilterCard';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import { makeTestQueryClient } from '../../../../test/queryClient';
import type { FilterGroup } from '../../../../types/schemas';

const apiRequest = vi.fn();
vi.mock('../../../../auth/apiRequest', () => ({
  apiRequest: (...args: unknown[]) => apiRequest(...args) as unknown,
}));

let serverFilter: FilterGroup[] = [];
let filterMode = true;
const previews: unknown[] = [];

function respond(path: string, opts: { json?: { filter: FilterGroup[] } }) {
  switch (path) {
    case '/api/v1/tracker/scope':
      return {
        data: {
          scope: {
            filter: serverFilter,
            filterMode,
            conditions: [
              'projects',
              'teams',
              'assignee',
              'labels_any',
              'labels_all',
              'labels_none',
              'priority',
            ],
          },
        },
      };
    case '/api/v1/tracker/filter/preview': {
      previews.push(opts.json?.filter);
      const n = opts.json?.filter.length ?? 0;
      return { data: { count: n * 3, identifiers: n > 0 ? ['ENG-1', 'ENG-2'] : [] } };
    }
    case '/api/v1/tracker/labels':
      return { data: { labels: ['agent', 'ai', 'blocked'] } };
    case '/api/v1/tracker/users':
      return { data: { users: [{ id: 'u-ada', name: 'Ada' }] } };
    case '/api/v1/tracker/teams':
      return { data: { teams: [{ id: 't-1', key: 'ENG', name: 'Engineering' }] } };
    case '/api/v1/projects':
      return { data: { projects: [{ id: 'p1', name: 'Eng', slug: 'eng' }] } };
  }
  throw new Error(`unexpected ${path}`);
}

function setup() {
  const onSave = vi.fn((filter: FilterGroup[]) => {
    serverFilter = filter;
    return Promise.resolve(true);
  });
  render(
    <QueryClientProvider client={makeTestQueryClient()}>
      <TrackerFilterCard onSave={onSave} />
    </QueryClientProvider>,
  );
  return { onSave };
}

describe('TrackerFilterCard (#198)', () => {
  beforeEach(() => {
    serverFilter = [
      { projects: ['eng'], labels_any: ['agent', 'ai'], labels_none: ['blocked'] },
      { assignee: 'me', labels_all: ['urgent'] },
    ];
    filterMode = true;
    previews.length = 0;
    apiRequest.mockReset();
    apiRequest.mockImplementation((path: string, opts: { json?: { filter: FilterGroup[] } }) =>
      Promise.resolve(respond(path, opts)),
    );
    useItervoxStore.setState({ snapshot: makeSnapshot() });
  });

  it('shows the groups ORed, their conditions ANDed, the summary and the live preview count', async () => {
    setup();
    const g1 = await screen.findByRole('group', { name: 'Group 1' });
    const g2 = screen.getByRole('group', { name: 'Group 2' });
    expect(within(g1).getAllByText('AND')).toHaveLength(2);
    expect(within(g2).getAllByText('AND')).toHaveLength(1);
    expect(screen.getByText('OR')).toBeInTheDocument();
    expect(screen.getByTestId('filter-summary')).toHaveTextContent(
      'eng · agent|ai · ¬blocked OR me · urgent',
    );
    await waitFor(() => {
      expect(screen.getByTestId('filter-preview')).toHaveTextContent(
        'Preview: 6 issues in the active states match (ENG-1, ENG-2, …).',
      );
    });
    expect(previews[0]).toEqual(serverFilter);
    expect(screen.getByRole('button', { name: 'Save filter' })).toBeDisabled();
  });

  it('adds a group with a condition, previews the edit (debounced) and saves the cleaned filter', async () => {
    const { onSave } = setup();
    await screen.findByRole('group', { name: 'Group 2' });
    fireEvent.click(screen.getByRole('button', { name: '+ Add group (OR)' }));
    const g3 = screen.getByRole('group', { name: 'Group 3' });
    expect(within(g3).getByText(/an empty group would match every issue/)).toBeInTheDocument();
    expect(screen.getByTestId('filter-preview')).toHaveTextContent('finish every group');
    expect(screen.getByRole('button', { name: 'Save filter' })).toBeDisabled();

    fireEvent.change(within(g3).getByRole('combobox', { name: 'Add a condition to Group 3' }), {
      target: { value: 'priority' },
    });
    fireEvent.click(within(g3).getByRole('checkbox', { name: 'P1 Urgent' }));
    fireEvent.click(within(g3).getByRole('checkbox', { name: 'P2 High' }));
    // A condition added but left blank is dropped on save, not sent as "".
    fireEvent.change(within(g3).getByRole('combobox', { name: 'Add a condition to Group 3' }), {
      target: { value: 'labels_none' },
    });

    // Remove a condition from group 1: labels_none.
    const g1 = screen.getByRole('group', { name: 'Group 1' });
    fireEvent.click(
      within(g1).getByRole('button', { name: 'Remove Labels (none of) from Group 1' }),
    );

    const want: FilterGroup[] = [
      { projects: ['eng'], labels_any: ['agent', 'ai'] },
      { assignee: 'me', labels_all: ['urgent'] },
      { priority: [1, 2] },
    ];
    await waitFor(
      () => {
        expect(previews.at(-1)).toEqual(want);
      },
      { timeout: 3000 },
    );
    await waitFor(() => {
      expect(screen.getByTestId('filter-preview')).toHaveTextContent('Preview: 9 issues');
    });

    fireEvent.click(screen.getByRole('button', { name: 'Save filter' }));
    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith(want);
    });
  });

  it('removes a group and picks values from the tracker choices', async () => {
    const { onSave } = setup();
    await screen.findByRole('group', { name: 'Group 2' });
    fireEvent.click(screen.getByRole('button', { name: 'Remove Group 2' }));
    expect(screen.queryByRole('group', { name: 'Group 2' })).toBeNull();
    const g1 = screen.getByRole('group', { name: 'Group 1' });
    fireEvent.change(within(g1).getByRole('combobox', { name: 'Add a condition to Group 1' }), {
      target: { value: 'teams' },
    });
    fireEvent.click(await within(g1).findByRole('button', { name: /ENG/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Save filter' }));
    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith([
        {
          projects: ['eng'],
          teams: ['ENG'],
          labels_any: ['agent', 'ai'],
          labels_none: ['blocked'],
        },
      ]);
    });
  });

  it('starts from the legacy keys as one group and saves an empty filter as "every issue"', async () => {
    serverFilter = [{ projects: ['eng'], assignee: 'me' }];
    filterMode = false;
    const { onSave } = setup();
    await screen.findByText(/Shown from the legacy project/);
    fireEvent.click(screen.getByRole('button', { name: 'Remove Group 1' }));
    expect(screen.getByText('No filter — every issue is read.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Save filter' }));
    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith([]);
    });
  });
});
