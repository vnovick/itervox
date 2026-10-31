import { FilterPills, type FilterPill } from '../../../components/itervox/FilterPills';
import { SearchInput } from '../../../components/itervox/SearchInput';
import { dashboardViewModes, type DashboardViewMode, viewModeLabel } from './viewModeLabel';

interface DashboardIssuesHeaderProps {
  filteredCount: number;
  availableProfileCount: number;
  notificationsTotal: number;
  viewMode: DashboardViewMode;
  setViewMode: (mode: DashboardViewMode) => void;
  loading: boolean;
  onRefresh: () => void;
  filterPills: FilterPill[];
  stateFilter: string;
  setStateFilter: (filter: string) => void;
  search: string;
  setSearch: (search: string) => void;
}

export function DashboardIssuesHeader({
  filteredCount,
  availableProfileCount,
  notificationsTotal,
  viewMode,
  setViewMode,
  loading,
  onRefresh,
  filterPills,
  stateFilter,
  setStateFilter,
  search,
  setSearch,
}: DashboardIssuesHeaderProps) {
  const filtersShown = viewMode !== 'agents' && viewMode !== 'deps' && viewMode !== 'notifications';
  return (
    <div className="flex flex-wrap items-center gap-2.5 px-1">
      <h2 className="text-theme-text mr-1.5 flex items-baseline gap-1.5 text-base font-semibold">
        Issues
        <span className="text-theme-muted font-medium tabular-nums">{filteredCount}</span>
      </h2>
      <div role="group" aria-label="Issue view" className="segmented flex-wrap">
        {dashboardViewModes(availableProfileCount > 0).map((mode) => (
          <button
            key={mode}
            type="button"
            aria-pressed={viewMode === mode}
            onClick={() => {
              setViewMode(mode);
            }}
            className="min-h-8 py-1 text-sm"
          >
            {viewModeLabel(mode, notificationsTotal)}
          </button>
        ))}
      </div>

      {filtersShown && (
        <FilterPills pills={filterPills} activeId={stateFilter} onChange={setStateFilter} />
      )}

      <div className="ml-auto flex w-full min-w-0 items-center gap-2 sm:w-auto">
        {filtersShown && (
          <SearchInput
            placeholder="Search identifier or title..."
            label="Search issues"
            value={search}
            onChange={setSearch}
            className="flex-1 sm:w-[240px] sm:flex-none"
            inputClassName="h-10"
          />
        )}
        <button
          type="button"
          onClick={onRefresh}
          disabled={loading}
          className="glass-chip text-theme-text-secondary hover:text-theme-text ml-auto flex h-9 w-9 flex-shrink-0 items-center justify-center text-sm transition-colors disabled:opacity-50"
          title={loading ? 'Refreshing...' : 'Refresh issues'}
          aria-label="Refresh issues"
        >
          {loading ? '...' : '↻'}
        </button>
      </div>
    </div>
  );
}
