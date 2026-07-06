import { fireEvent, render, screen } from '@testing-library/react';
import type React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { DashboardIssuesHeader } from '../DashboardIssuesHeader';
import type { DashboardViewMode } from '../viewModeLabel';

const filterPills = [{ id: 'all', label: 'All Issues', states: [] }];

function renderHeader(overrides: Partial<React.ComponentProps<typeof DashboardIssuesHeader>> = {}) {
  const setViewMode = vi.fn();
  const props: React.ComponentProps<typeof DashboardIssuesHeader> = {
    filteredCount: 3,
    availableProfileCount: 1,
    notificationsTotal: 2,
    viewMode: 'board',
    setViewMode,
    loading: false,
    onRefresh: vi.fn(),
    filterPills,
    stateFilter: 'all',
    setStateFilter: vi.fn(),
    search: '',
    setSearch: vi.fn(),
    ...overrides,
  };

  render(<DashboardIssuesHeader {...props} />);
  return { setViewMode };
}

describe('DashboardIssuesHeader', () => {
  it('exposes the Deps tab as a keyboard-focusable button after Agents', () => {
    const { setViewMode } = renderHeader();
    const buttons = screen.getAllByRole('button');
    const labels = buttons.map((button) => button.textContent);

    expect(labels.slice(0, 5)).toEqual(['Board', 'List', 'Agents', 'Deps', 'Notifications · 2']);

    const depsButton = screen.getByRole('button', { name: 'Deps' });
    expect(depsButton).toHaveAttribute('type', 'button');
    expect(depsButton).toHaveAttribute('aria-pressed', 'false');

    fireEvent.click(depsButton);
    expect(setViewMode).toHaveBeenCalledWith('deps');
  });

  it('marks the active Deps tab for assistive technology', () => {
    renderHeader({ viewMode: 'deps' as DashboardViewMode });

    expect(screen.getByRole('button', { name: 'Deps' })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByLabelText('Search issues')).not.toBeInTheDocument();
  });
});
