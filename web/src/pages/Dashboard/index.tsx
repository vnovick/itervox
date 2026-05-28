import { useState, useMemo, useCallback } from 'react';
import { useConnectionState } from '../../hooks/useConnectionState';
import { useShallow } from 'zustand/react/shallow';
import PageMeta from '../../components/common/PageMeta';
import RunningSessionsTable from '../../components/itervox/RunningSessionsTable';
import RetryQueueTable from '../../components/itervox/RetryQueueTable';
import { PendingResumePanel } from '../../components/itervox/PendingResumePanel';
import { ReviewQueueSection } from '../../components/itervox/ReviewQueueSection';
import { HostPool } from '../../components/itervox/HostPool';
import { ProjectSelector } from '../../components/itervox/ProjectSelector';
import { NarrativeFeed } from '../../components/itervox/NarrativeFeed';
import { useItervoxStore } from '../../store/itervoxStore';
import { useIssues, useUpdateIssueState, useSetIssueProfile } from '../../queries/issues';
import { HeroStats } from './components/HeroStats';
import { LiveOpsStrip } from './components/LiveOpsStrip';
import { AutomationQueueList } from './components/AutomationQueueList';
import { AutomationQueueDetailPanel } from './components/AutomationQueueDetailPanel';
import { DashboardIssuesPanel } from './components/DashboardIssuesPanel';

// v0.2.0 audit P2-9 — typed empty arrays now live in `utils/constants.ts`
// alongside the other module-level stable references. The three local
// aliases (BACKLOG/ACTIVE/TERMINAL → EMPTY_STATES) are gone; callers below
// just use `EMPTY_STATES` directly.
import {
  EMPTY_PROFILES,
  EMPTY_STATES,
  EMPTY_RUNNING,
  EMPTY_HISTORY,
  EMPTY_HOSTS,
  EMPTY_AUTOMATIONS,
  EMPTY_DEPS_NODES,
  EMPTY_DEPS_EDGES,
  EMPTY_AUTOMATION_QUEUE,
  EMPTY_DEPENDENCY_AUDIT,
} from '../../utils/constants';

export default function Dashboard() {
  const { data: issues = [] } = useIssues();
  const {
    availableProfiles,
    backlogStates,
    activeStates,
    terminalStates,
    completionState,
    profileDefs,
    availableModels,
    supportedAgentActions,
    defaultBackend,
    running,
    runHistory,
    maxConcurrentAgents,
    sshHosts,
    automations,
    dependencyGraphNodes,
    dependencyGraphEdges,
    automationQueue,
    automationQueueBackpressure,
    dependencyAudit,
  } = useItervoxStore(
    useShallow((s) => ({
      availableProfiles: s.snapshot?.availableProfiles ?? EMPTY_PROFILES,
      backlogStates: s.snapshot?.backlogStates ?? EMPTY_STATES,
      activeStates: s.snapshot?.activeStates ?? EMPTY_STATES,
      terminalStates: s.snapshot?.terminalStates ?? EMPTY_STATES,
      completionState: s.snapshot?.completionState ?? '',
      profileDefs: s.snapshot?.profileDefs,
      availableModels: s.snapshot?.availableModels,
      supportedAgentActions: s.snapshot?.supportedAgentActions,
      defaultBackend: s.snapshot?.defaultBackend,
      running: s.snapshot?.running ?? EMPTY_RUNNING,
      runHistory: s.snapshot?.history ?? EMPTY_HISTORY,
      maxConcurrentAgents: s.snapshot?.maxConcurrentAgents ?? 0,
      sshHosts: s.snapshot?.sshHosts ?? EMPTY_HOSTS,
      automations: s.snapshot?.automations ?? EMPTY_AUTOMATIONS,
      dependencyGraphNodes: s.snapshot?.dependencyGraphNodes ?? EMPTY_DEPS_NODES,
      dependencyGraphEdges: s.snapshot?.dependencyGraphEdges ?? EMPTY_DEPS_EDGES,
      automationQueue: s.snapshot?.automationQueue ?? EMPTY_AUTOMATION_QUEUE,
      automationQueueBackpressure: s.snapshot?.automationQueueBackpressure,
      dependencyAudit: s.snapshot?.dependencyAudit ?? EMPTY_DEPENDENCY_AUDIT,
    })),
  );
  const backlogStateSet = useMemo(() => new Set(backlogStates), [backlogStates]);
  const runningBackendByIdentifier = useMemo(() => {
    const map: Record<string, string> = {};
    const backlogIdentifiers = new Set(
      issues.filter((i) => backlogStateSet.has(i.state)).map((i) => i.identifier),
    );
    for (const h of runHistory) {
      if (h.backend && !backlogIdentifiers.has(h.identifier)) map[h.identifier] = h.backend;
    }
    for (const r of running) {
      if (r.backend) map[r.identifier] = r.backend;
    }
    return map;
  }, [running, runHistory, issues, backlogStateSet]);

  const setSelectedIdentifier = useItervoxStore((s) => s.setSelectedIdentifier);
  const { mutateAsync: updateIssueState } = useUpdateIssueState();
  const setIssueProfileMutation = useSetIssueProfile();

  // v0.2.0 audit P2-10 — connection-state timing is centralised in
  // useConnectionState (8s timeout, single source of truth shared with
  // AppHeader).
  const { isOffline: apiOffline } = useConnectionState();
  const [selectedQueueId, setSelectedQueueId] = useState<string | null>(null);
  const handleIssueSelect = useCallback(
    (identifier: string) => {
      setSelectedIdentifier(identifier);
    },
    [setSelectedIdentifier],
  );
  const handleQueueSelect = useCallback((queueId: string) => {
    setSelectedQueueId(queueId);
  }, []);
  const selectedQueueRow = useMemo(
    () => automationQueue.find((item) => item.id === selectedQueueId) ?? null,
    [automationQueue, selectedQueueId],
  );
  const selectedQueueAutomation = useMemo(
    () =>
      selectedQueueRow
        ? automations.find((automation) => automation.id === selectedQueueRow.automationId)
        : undefined,
    [automations, selectedQueueRow],
  );
  const selectedQueueDependency = useMemo(
    () =>
      selectedQueueRow
        ? dependencyAudit.find((row) => row.identifier === selectedQueueRow.identifier)
        : undefined,
    [dependencyAudit, selectedQueueRow],
  );

  const handleStateChange = useCallback(
    async (identifier: string, newState: string) => {
      try {
        await updateIssueState({ identifier, state: newState });
      } catch {
        // mutation's onError already rolls back optimistic update
      }
    },
    [updateIssueState],
  );

  const handleProfileChange = useCallback(
    (identifier: string, profile: string) => {
      setIssueProfileMutation.mutate({ identifier, profile });
    },
    [setIssueProfileMutation],
  );

  return (
    <>
      <PageMeta
        title="Itervox | Dashboard"
        description="Itervox — autonomous agentic harness for multi-agent collaboration, observability, human input, and fleet distribution"
      />
      <div className="space-y-[14px]">
        <ProjectSelector />
        <LiveOpsStrip />

        {/* Hero-compact banner — responsive: stacks on mobile */}
        <div className="border-theme-line bg-theme-bg-elevated relative overflow-hidden rounded-[var(--radius-lg)] border px-4 py-4 sm:px-[22px] sm:py-[18px]">
          <div
            className="pointer-events-none absolute inset-0"
            style={{
              background:
                'radial-gradient(ellipse at top left, var(--accent-soft) 0%, transparent 60%)',
            }}
          />
          <div className="relative z-10 flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between lg:gap-6">
            <div className="min-w-0">
              <div className="mb-2">
                <span className="bg-theme-accent-soft text-theme-accent-strong inline-flex items-center rounded-full px-3 py-[5px] text-[11px] font-semibold tracking-[0.03em] uppercase">
                  Itervox
                </span>
              </div>
              <h1
                className="text-xl leading-tight font-bold tracking-[-0.03em] sm:text-2xl"
                style={{
                  background: 'var(--gradient-accent)',
                  WebkitBackgroundClip: 'text',
                  WebkitTextFillColor: 'transparent',
                }}
              >
                Autonomous agentic harness
              </h1>
              <p className="text-theme-text-secondary mt-2 text-[13px] leading-relaxed">
                Multi-agent collaboration, observability, human input, and fleet distribution.
              </p>
            </div>
            <HeroStats />
          </div>
        </div>

        <AutomationQueueList
          queue={automationQueue}
          backpressure={automationQueueBackpressure}
          dependencyAudit={dependencyAudit}
          onSelectIssue={handleIssueSelect}
          onSelectQueue={handleQueueSelect}
        />

        {apiOffline && (
          <div className="border-theme-warning-soft bg-theme-warning-soft text-theme-warning rounded-[var(--radius-md)] border p-4 text-sm">
            <p className="mb-1 font-semibold">Cannot reach the Itervox API</p>
            <p className="mb-2 opacity-80">
              Make sure your{' '}
              <code className="bg-theme-bg-elevated rounded px-1 font-mono">WORKFLOW.md</code> front
              matter includes the following and the itervox binary is running:
            </p>
            <pre className="bg-theme-bg-elevated rounded p-2 font-mono text-xs">
              {'server:\n  port: 8090'}
            </pre>
          </div>
        )}

        <HostPool />
        <RunningSessionsTable />
        <ReviewQueueSection />
        <PendingResumePanel onSelect={handleIssueSelect} />
        <RetryQueueTable />

        <DashboardIssuesPanel
          issues={issues}
          activeStates={activeStates}
          backlogStates={backlogStates}
          terminalStates={terminalStates}
          completionState={completionState}
          availableProfiles={availableProfiles}
          profileDefs={profileDefs}
          availableModels={availableModels}
          supportedAgentActions={supportedAgentActions}
          runningBackendByIdentifier={runningBackendByIdentifier}
          defaultBackend={defaultBackend}
          dependencyGraphNodes={dependencyGraphNodes}
          dependencyGraphEdges={dependencyGraphEdges}
          onIssueSelect={handleIssueSelect}
          onStateChange={handleStateChange}
          onProfileChange={handleProfileChange}
        />

        <NarrativeFeed />
      </div>
      <AutomationQueueDetailPanel
        row={selectedQueueRow}
        automation={selectedQueueAutomation}
        dependency={selectedQueueDependency}
        profileDef={selectedQueueRow ? profileDefs?.[selectedQueueRow.profile] : undefined}
        running={running}
        maxConcurrentAgents={maxConcurrentAgents}
        sshHosts={sshHosts}
        onClose={() => {
          setSelectedQueueId(null);
        }}
      />
    </>
  );
}
