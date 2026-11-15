import { Suspense, lazy } from 'react';
import AgentQueueView from '../../../components/itervox/AgentQueueView';
import { NotificationsView } from '../../../components/itervox/NotificationsView';
import type {
  DependencyCycleRow,
  DependencyGraphEdge,
  DependencyGraphNode,
  DepsAnalyzeJob,
  ProfileDef,
  StateSnapshot,
  TrackerIssue,
} from '../../../types/schemas';
import { BoardView } from './BoardView';
import { ListView } from './ListView';
import type { DashboardViewMode } from './viewModeLabel';

// #211 — DepsGraph pulls in @xyflow/react (and its stylesheet), the largest
// single dependency of the Dashboard route. Load it only when the Deps tab
// opens so the Dashboard chunk stays free of it.
const DepsGraph = lazy(() => import('./DepsGraph').then((m) => ({ default: m.DepsGraph })));

function DepsGraphFallback() {
  return (
    <div
      data-testid="deps-graph-loading"
      role="status"
      className="text-theme-muted py-8 text-center text-sm"
    >
      Loading dependency graph…
    </div>
  );
}

interface DashboardIssueViewsProps {
  viewMode: DashboardViewMode;
  filteredIssues: TrackerIssue[];
  allIssues: TrackerIssue[];
  backlogStates: string[];
  availableProfiles: string[];
  profileDefs?: Record<string, ProfileDef>;
  availableModels?: StateSnapshot['availableModels'];
  supportedAgentActions?: StateSnapshot['supportedAgentActions'];
  runningBackendByIdentifier: Record<string, string>;
  defaultBackend?: string;
  dependencyGraphNodes: DependencyGraphNode[];
  dependencyGraphEdges: DependencyGraphEdge[];
  dependencyCycles?: DependencyCycleRow[];
  depsAnalyzerProfile?: string;
  depsLastAnalyzedAt?: string;
  depsAnalyzeJob?: DepsAnalyzeJob;
  onIssueSelect: (identifier: string) => void;
  onStateChange: (identifier: string, newState: string) => void;
  onProfileChange: (identifier: string, profile: string) => void;
  onEditProfile: (name: string, def: ProfileDef & { originalName?: string }) => Promise<void>;
}

export function DashboardIssueViews({
  viewMode,
  filteredIssues,
  allIssues,
  backlogStates,
  availableProfiles,
  profileDefs,
  availableModels,
  supportedAgentActions,
  runningBackendByIdentifier,
  defaultBackend,
  dependencyGraphNodes,
  dependencyGraphEdges,
  dependencyCycles,
  depsAnalyzerProfile,
  depsLastAnalyzedAt,
  depsAnalyzeJob,
  onIssueSelect,
  onStateChange,
  onProfileChange,
  onEditProfile,
}: DashboardIssueViewsProps) {
  return (
    <div className="px-4 pt-3 pb-4 sm:px-[18px] sm:pt-[14px] sm:pb-[18px]">
      {viewMode === 'board' && (
        <BoardView
          issues={filteredIssues}
          onSelect={onIssueSelect}
          onStateChange={onStateChange}
          availableProfiles={availableProfiles}
          onProfileChange={onProfileChange}
        />
      )}
      {viewMode === 'list' && (
        <ListView
          issues={filteredIssues}
          onSelect={onIssueSelect}
          availableProfiles={availableProfiles}
          profileDefs={profileDefs}
          runningBackendByIdentifier={runningBackendByIdentifier}
          defaultBackend={defaultBackend}
          backlogStates={backlogStates}
          onProfileChange={onProfileChange}
        />
      )}
      {viewMode === 'agents' && (
        <div className="-mx-4 overflow-x-auto px-4 pb-2 md:-mx-6 md:px-6">
          <AgentQueueView
            issues={allIssues}
            backlogStates={backlogStates}
            availableProfiles={availableProfiles}
            profileDefs={profileDefs}
            availableModels={availableModels}
            supportedAgentActions={supportedAgentActions}
            onProfileChange={onProfileChange}
            onSelect={onIssueSelect}
            onEditProfile={onEditProfile}
          />
        </div>
      )}
      {viewMode === 'deps' && (
        <Suspense fallback={<DepsGraphFallback />}>
          <DepsGraph
            graphNodes={dependencyGraphNodes}
            graphEdges={dependencyGraphEdges}
            cycles={dependencyCycles}
            onSelectIssue={onIssueSelect}
            depsAnalyzerProfile={depsAnalyzerProfile}
            depsLastAnalyzedAt={depsLastAnalyzedAt}
            depsAnalyzeJob={depsAnalyzeJob}
            profileDefs={profileDefs}
          />
        </Suspense>
      )}
      {viewMode === 'notifications' && <NotificationsView onSelect={onIssueSelect} />}
    </div>
  );
}
