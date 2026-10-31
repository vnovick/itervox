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
import { OutboxList } from './components/OutboxList';
import { FailuresPanel } from './components/FailuresPanel';
import { DashboardIssuesPanel } from './components/DashboardIssuesPanel';
import { AttentionInbox } from './components/AttentionInbox';
import { FirstRunChecklist } from './components/FirstRunChecklist';
import { useDashboardUrlState, useNewTaskUrlOpen } from '../../hooks/useUrlState';
import { NewTaskDialog } from '../../components/itervox/newTask/NewTaskDialog';
import { useHashScroll } from '../../hooks/useHashScroll';

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
  EMPTY_DEPENDENCY_CYCLES,
  EMPTY_OUTBOX_ENTRIES,
  EMPTY_RECENT_FAILURES,
} from '../../utils/constants';
import type { TrackerIssue } from '../../types/schemas';

// #211 — stable fallback so the memoised board keeps its props while the
// issue list is still loading.
const EMPTY_ISSUES: TrackerIssue[] = [];

export default function Dashboard() {
  // CORE-079 — ?view= / ?q= / ?state= <-> the dashboard view and filters.
  useDashboardUrlState();
  useNewTaskUrlOpen(); // #251: ?newTask=1 (the Mac app's New Task… item)
  // CORE-086 — header pills and ops chips link to sections by hash; scroll
  // once the targeted section has rows (the key changes when any does).
  const sectionRowsKey = useItervoxStore((st) => {
    const snap = st.snapshot;
    if (!snap) return '';
    return [
      snap.running.length,
      snap.paused.length,
      snap.retrying.length,
      snap.inputRequired?.length ?? 0,
      snap.outboxEntries?.length ?? 0,
    ].join('|');
  });
  useHashScroll(sectionRowsKey);
  const { data: issues = EMPTY_ISSUES } = useIssues();
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
    depsAnalyzerProfile,
    depsLastAnalyzedAt,
    depsAnalyzeJob,
    automationQueue,
    automationQueueBackpressure,
    dependencyAudit,
    dependencyCycles,
    outboxEntries,
    recentFailures,
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
      depsAnalyzerProfile: s.snapshot?.depsAnalyzerProfile,
      depsLastAnalyzedAt: s.snapshot?.depsLastAnalyzedAt,
      depsAnalyzeJob: s.snapshot?.depsAnalyzeJob,
      automationQueue: s.snapshot?.automationQueue ?? EMPTY_AUTOMATION_QUEUE,
      automationQueueBackpressure: s.snapshot?.automationQueueBackpressure,
      dependencyAudit: s.snapshot?.dependencyAudit ?? EMPTY_DEPENDENCY_AUDIT,
      dependencyCycles: s.snapshot?.dependencyCycles ?? EMPTY_DEPENDENCY_CYCLES,
      outboxEntries: s.snapshot?.outboxEntries ?? EMPTY_OUTBOX_ENTRIES,
      recentFailures: s.snapshot?.recentFailures ?? EMPTY_RECENT_FAILURES,
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
  // #211 — depend on the stable `mutate`, not the mutation result object
  // (a fresh object every render), so handleProfileChange keeps its identity
  // and the memoised BoardView skips renders for unrelated snapshot fields.
  const { mutate: setIssueProfile } = useSetIssueProfile();

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
      setIssueProfile({ identifier, profile });
    },
    [setIssueProfile],
  );

  return (
    <>
      <PageMeta
        title="Itervox | Dashboard"
        description="Itervox — autonomous agentic harness for multi-agent collaboration, observability, human input, and fleet distribution"
      />
      <NewTaskDialog />
      <div className="page-container">
        <ProjectSelector />
        {/* CORE-077 — everything that needs the operator, with inline
            Reply / Resume / Discard, above every other section. */}
        {/* CORE-097 — only on an empty fleet (no issues, no runs). */}
        <FirstRunChecklist />
        <AttentionInbox onSelectIssue={handleIssueSelect} />
        <LiveOpsStrip />

        {/* Hero: product line + the live stat tiles. */}
        <section
          aria-label="Overview"
          className="glass-panel flex flex-col gap-4 rounded-[var(--radius-shell)] px-4 py-5 sm:px-6"
        >
          <div className="min-w-0">
            <span className="glass-chip inline-flex items-center px-2.5 py-[3px] text-xs font-semibold">
              Itervox
            </span>
            <h1 className="page-title mt-2.5 mb-1 text-[26px] sm:text-[30px]">
              Autonomous agentic harness
            </h1>
            <p className="text-theme-text-secondary text-sm">
              Multi-agent collaboration, observability, human input, and fleet distribution.
            </p>
          </div>
          <HeroStats />
        </section>

        {/* Wide screens: Running Sessions beside the Automation Queue and
            the Host pool; a section that renders nothing frees its column
            (empty:hidden), and narrow screens stack as before. */}
        <div className="flex flex-col gap-4 xl:flex-row xl:items-start">
          <div className="min-w-0 empty:hidden xl:flex-1">
            <RunningSessionsTable />
          </div>
          <div className="flex min-w-0 flex-col gap-4 empty:hidden xl:flex-1">
            <AutomationQueueList
              queue={automationQueue}
              running={running}
              backpressure={automationQueueBackpressure}
              dependencyAudit={dependencyAudit}
              onSelectIssue={handleIssueSelect}
              onSelectQueue={handleQueueSelect}
            />
            <HostPool />
          </div>
        </div>

        <OutboxList entries={outboxEntries} onSelectIssue={handleIssueSelect} />

        <FailuresPanel failures={recentFailures} onSelectIssue={handleIssueSelect} />

        {apiOffline && (
          <div className="banner-warn p-4">
            <p className="mb-1 font-semibold">Cannot reach the Itervox API</p>
            {/* CORE-022 — the daemon's port is NOT reliably 8090: omitting
                server.port from WORKFLOW.md does default to 8090, but
                `itervox init` scaffolds `port: 0`, which asks the OS for a
                random free port instead. Tell the operator how to find the
                real URL rather than a port that may not be in use. */}
            <p className="opacity-80">
              Make sure the itervox binary is running for this project. If{' '}
              <code className="subtle-fill rounded px-1 font-mono">server.port</code> is omitted
              from your <code className="subtle-fill rounded px-1 font-mono">WORKFLOW.md</code>{' '}
              front matter, the daemon listens on port 8090 by default — but{' '}
              <code className="subtle-fill rounded px-1 font-mono">itervox init</code> scaffolds{' '}
              <code className="subtle-fill rounded px-1 font-mono">port: 0</code>, which asks the OS
              for a random free port instead. Run{' '}
              <code className="subtle-fill rounded px-1 font-mono">itervox doctor</code> or check{' '}
              <code className="subtle-fill rounded px-1 font-mono">.itervox/dashboard_url</code> to
              find the URL it is actually listening on.
            </p>
          </div>
        )}

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
          dependencyCycles={dependencyCycles}
          depsAnalyzerProfile={depsAnalyzerProfile}
          depsLastAnalyzedAt={depsLastAnalyzedAt}
          depsAnalyzeJob={depsAnalyzeJob}
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
