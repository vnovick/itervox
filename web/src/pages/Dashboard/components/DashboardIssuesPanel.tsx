import { useCallback, useMemo } from 'react';
import { useMutation } from '@tanstack/react-query';
import type { FilterPill } from '../../../components/itervox/FilterPills';
import { authedFetch } from '../../../auth/authedFetch';
import { UnauthorizedError } from '../../../auth/UnauthorizedError';
import { useSettingsActions } from '../../../hooks/useSettingsActions';
import { useInvalidateIssues } from '../../../queries/issues';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useToastStore } from '../../../store/toastStore';
import { useUIStore } from '../../../store/uiStore';
import type {
  DependencyGraphEdge,
  DependencyGraphNode,
  ProfileDef,
  StateSnapshot,
  TrackerIssue,
} from '../../../types/schemas';
import { DashboardIssuesHeader } from './DashboardIssuesHeader';
import { DashboardIssueViews } from './DashboardIssueViews';
import { useNotificationsTotal } from './useNotificationsTotal';

interface DashboardIssuesPanelProps {
  issues: TrackerIssue[];
  activeStates: string[];
  backlogStates: string[];
  terminalStates: string[];
  completionState: string;
  availableProfiles: string[];
  profileDefs?: Record<string, ProfileDef>;
  availableModels?: StateSnapshot['availableModels'];
  supportedAgentActions?: StateSnapshot['supportedAgentActions'];
  runningBackendByIdentifier: Record<string, string>;
  defaultBackend?: string;
  dependencyGraphNodes: DependencyGraphNode[];
  dependencyGraphEdges: DependencyGraphEdge[];
  depsAnalyzerProfile?: string;
  depsLastAnalyzedAt?: string;
  onIssueSelect: (identifier: string) => void;
  onStateChange: (identifier: string, newState: string) => void;
  onProfileChange: (identifier: string, profile: string) => void;
}

export function DashboardIssuesPanel({
  issues,
  activeStates,
  backlogStates,
  terminalStates,
  completionState,
  availableProfiles,
  profileDefs,
  availableModels,
  supportedAgentActions,
  runningBackendByIdentifier,
  defaultBackend,
  dependencyGraphNodes,
  dependencyGraphEdges,
  depsAnalyzerProfile,
  depsLastAnalyzedAt,
  onIssueSelect,
  onStateChange,
  onProfileChange,
}: DashboardIssuesPanelProps) {
  const viewMode = useUIStore((s) => s.dashboardViewMode);
  const setViewMode = useUIStore((s) => s.setDashboardViewMode);
  const search = useUIStore((s) => s.dashboardSearch);
  const setSearch = useUIStore((s) => s.setDashboardSearch);
  const stateFilter = useUIStore((s) => s.dashboardStateFilter);
  const setStateFilter = useUIStore((s) => s.setDashboardStateFilter);
  const invalidateIssues = useInvalidateIssues();
  const { upsertProfile } = useSettingsActions();
  const notificationsTotal = useNotificationsTotal(issues);

  const filterPills = useMemo<FilterPill[]>(() => {
    const pills: FilterPill[] = [{ id: 'all', label: 'All Issues', states: [] }];
    if (activeStates.length > 0)
      pills.push({ id: 'active', label: 'Active', states: activeStates });
    if (backlogStates.length > 0)
      pills.push({ id: 'backlog', label: 'Backlog', states: backlogStates });
    if (completionState)
      pills.push({ id: 'review', label: completionState, states: [completionState] });
    if (terminalStates.length > 0)
      pills.push({ id: 'done', label: 'Done', states: terminalStates });
    return pills;
  }, [activeStates, backlogStates, terminalStates, completionState]);

  const activePillStates = useMemo(() => {
    if (stateFilter === 'all') return null;
    const pill = filterPills.find((p) => p.id === stateFilter);
    return pill?.states ?? null;
  }, [stateFilter, filterPills]);

  const filtered = useMemo(
    () =>
      issues.filter((issue) => {
        const q = search.trim().toLowerCase();
        if (
          q &&
          !issue.identifier.toLowerCase().includes(q) &&
          !issue.title.toLowerCase().includes(q)
        )
          return false;
        if (activePillStates !== null) {
          const match = activePillStates.some((s) => s.toLowerCase() === issue.state.toLowerCase());
          if (!match) return false;
        }
        return true;
      }),
    [issues, search, activePillStates],
  );

  // v0.2.0 audit P1-10 — refresh wraps three state-layer concerns (HTTP,
  // TanStack invalidation, Zustand snapshot refresh). Owning the loading
  // state in a useMutation centralises it in the query layer so future
  // maintainers do not have to reason about which `loading` flag is the
  // source of truth.
  const refreshMutation = useMutation({
    mutationFn: async () => {
      await authedFetch('/api/v1/refresh', { method: 'POST' });
      await useItervoxStore.getState().refreshSnapshot();
    },
    onSuccess: () => {
      void invalidateIssues();
    },
    onError: (err) => {
      if (!(err instanceof UnauthorizedError)) {
        useToastStore.getState().addToast('Refresh failed - check the server.', 'error');
      }
    },
  });
  const handleRefresh = useCallback(() => {
    refreshMutation.mutate();
  }, [refreshMutation]);
  const loading = refreshMutation.isPending;

  const handleEditProfile = useCallback(
    async (name: string, def: ProfileDef & { originalName?: string }) => {
      await upsertProfile(
        name,
        def.command,
        def.backend,
        def.prompt,
        def.soul,
        def.instructions,
        def.soulFile,
        def.instructionsFile,
        def.enabled,
        def.allowedActions,
        def.createIssueState,
        def.originalName,
      );
    },
    [upsertProfile],
  );

  return (
    <div className="border-theme-line bg-theme-bg-elevated shadow-theme-sm overflow-hidden rounded-[var(--radius-lg)] border">
      <DashboardIssuesHeader
        filteredCount={filtered.length}
        availableProfileCount={availableProfiles.length}
        notificationsTotal={notificationsTotal}
        viewMode={viewMode}
        setViewMode={setViewMode}
        loading={loading}
        onRefresh={handleRefresh}
        filterPills={filterPills}
        stateFilter={stateFilter}
        setStateFilter={setStateFilter}
        search={search}
        setSearch={setSearch}
      />
      <DashboardIssueViews
        viewMode={viewMode}
        filteredIssues={filtered}
        allIssues={issues}
        backlogStates={backlogStates}
        availableProfiles={availableProfiles}
        profileDefs={profileDefs}
        availableModels={availableModels}
        supportedAgentActions={supportedAgentActions}
        runningBackendByIdentifier={runningBackendByIdentifier}
        defaultBackend={defaultBackend}
        dependencyGraphNodes={dependencyGraphNodes}
        dependencyGraphEdges={dependencyGraphEdges}
        depsAnalyzerProfile={depsAnalyzerProfile}
        depsLastAnalyzedAt={depsLastAnalyzedAt}
        onIssueSelect={onIssueSelect}
        onStateChange={onStateChange}
        onProfileChange={onProfileChange}
        onEditProfile={handleEditProfile}
      />
    </div>
  );
}
