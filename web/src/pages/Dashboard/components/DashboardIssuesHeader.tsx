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
  return (
    <div className="border-theme-line flex flex-col gap-3 border-b px-4 py-3 sm:px-[18px] sm:py-[14px]">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-theme-text flex items-center gap-2 text-sm font-semibold tracking-tight">
            Issues
            <span className="bg-theme-bg-soft text-theme-text-secondary rounded-full px-1.5 py-0.5 text-[10px] font-bold">
              {filteredCount}
            </span>
          </h2>
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
          <div className="bg-theme-bg-elevated border-theme-line inline-flex items-center gap-0.5 rounded-[var(--radius-md)] border p-[3px]">
            {dashboardViewModes(availableProfileCount > 0).map((mode) => (
              <button
                key={mode}
                type="button"
                aria-pressed={viewMode === mode}
                onClick={() => {
                  setViewMode(mode);
                }}
                className={`rounded-[var(--radius-sm)] px-3 py-1.5 text-xs font-semibold transition-all ${
                  viewMode === mode ? 'bg-theme-accent text-white' : 'text-theme-muted'
                }`}
              >
                {viewModeLabel(mode, notificationsTotal)}
              </button>
            ))}
          </div>

          <button
            onClick={onRefresh}
            disabled={loading}
            className="border-theme-line text-theme-text-secondary flex h-7 w-7 items-center justify-center rounded-lg border text-sm transition-colors disabled:opacity-50"
            title={loading ? 'Refreshing...' : 'Refresh issues'}
            aria-label="Refresh issues"
          >
            {loading ? '...' : '↻'}
          </button>
        </div>
      </div>

      {viewMode !== 'agents' && viewMode !== 'deps' && viewMode !== 'notifications' && (
        <>
          <FilterPills pills={filterPills} activeId={stateFilter} onChange={setStateFilter} />
          <SearchInput
            placeholder="Search identifier or title..."
            label="Search issues"
            value={search}
            onChange={setSearch}
            className="flex-1"
          />
        </>
      )}
    </div>
  );
}
