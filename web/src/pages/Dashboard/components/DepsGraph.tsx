import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Background,
  Controls,
  ReactFlow,
  type Edge,
  type Node,
  type NodeMouseHandler,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import {
  useAnalyzeDeps,
  useCancelAnalyzeDeps,
  useSetDepsOverride,
  type DepsJobUpdate,
} from '../../../queries/deps';
import type {
  DependencyCycleRow,
  DependencyGraphEdge,
  DependencyGraphNode,
  DepsAnalyzeJob,
  ProfileDef,
} from '../../../types/schemas';

const NODE_WIDTH = 220;
const ROW_HEIGHT = 118;
const LEFT_X = 0;
const RIGHT_X = 330;

/**
 * Position dependency nodes in two columns: blocker sources on the left,
 * blocked targets on the right.
 *
 * v0.2.0 audit P1-7 — the previous implementation called `findIndex` inside
 * the `.map` over every node, producing O(N²) lookups. We now build an index
 * once and look up in O(1). The call site memoises so layout only re-runs
 * when nodes/edges actually change, not on every snapshot tick.
 */
// findCycleForEdge returns the cycle (strongly-connected-component or
// self-edge) that an edge belongs to, i.e. the one whose members include
// BOTH the edge's source and target — not just either side. A blocker that
// is merely a member of some unrelated cycle should not be highlighted;
// only edges that actually close the loop are. critical-path-ordering
// Task 6.
function findCycleForEdge(
  cycles: readonly DependencyCycleRow[],
  sourceIdentifier: string,
  targetIdentifier: string,
): DependencyCycleRow | undefined {
  return cycles.find(
    (cycle) => cycle.members.includes(sourceIdentifier) && cycle.members.includes(targetIdentifier),
  );
}

// describeCycle is the single source of truth for a cycle's human-readable
// text — members joined by an arrow, plus (for an `inferred`-kind cycle) an
// explicit caveat that analyzer-derived cycles are more likely to be a false
// positive than a tracker-declared one, pointing the operator at the two
// remediation paths this component already exposes: the per-issue override
// panel below, or re-running "Analyze dependencies" above.
//
// critical-path-ordering Task 6 review fix — this text previously lived only
// on `edge.data.cycleTooltip`, which nothing in the render tree consumed
// (React Flow's default edge type has no hover-tooltip surface without a
// custom edge component). `DepsCycleBanner` below is the actual
// visible/testable surface; per-edge highlighting stays limited to the
// danger stroke + `[cycle]` badge token (see layoutDependencyGraph), which
// tells an operator THAT an edge is cyclic without naming members.
function describeCycle(cycle: DependencyCycleRow): string {
  const membersText = cycle.members.join(' → ');
  return cycle.kind === 'inferred'
    ? `${membersText} — likely an analyzer artifact; consider a per-issue override or re-running dependency analysis.`
    : membersText;
}

export function layoutDependencyGraph(
  graphNodes: readonly DependencyGraphNode[],
  graphEdges: readonly DependencyGraphEdge[],
  cycles: readonly DependencyCycleRow[] = [],
): { nodes: Node[]; edges: Edge[] } {
  const targetIds = new Set(graphEdges.map((edge) => edge.targetIdentifier));
  const leftNodes = graphNodes.filter((node) => !targetIds.has(node.identifier));
  const rightNodes = graphNodes.filter((node) => targetIds.has(node.identifier));

  const leftIndex = new Map(leftNodes.map((node, i) => [node.identifier, i]));
  const rightIndex = new Map(rightNodes.map((node, i) => [node.identifier, i]));

  const positioned = [...leftNodes, ...rightNodes].map((node) => {
    const inRightColumn = targetIds.has(node.identifier);
    const columnIndex = inRightColumn
      ? (rightIndex.get(node.identifier) ?? 0)
      : (leftIndex.get(node.identifier) ?? 0);
    return {
      id: node.id,
      type: 'default',
      position: {
        x: inRightColumn ? RIGHT_X : LEFT_X,
        y: Math.max(0, columnIndex) * ROW_HEIGHT,
      },
      data: {
        label: <IssueGraphLabel node={node} />,
        identifier: node.identifier,
      },
      style: {
        width: NODE_WIDTH,
        borderRadius: 8,
        border: '1px solid var(--line)',
        background: 'var(--bg-elevated)',
        color: 'var(--text)',
      },
    } satisfies Node;
  });

  return {
    nodes: positioned,
    edges: graphEdges.map((edge) => {
      const isInferred = edge.origin === 'inferred';
      // Surface the agent's `evidence` string on hover
      // for inferred edges. React Flow renders edge labels as <text> nodes
      // inside the SVG; wrapping the visible string in a span with the
      // evidence as the `title` attribute would require a custom edge type,
      // so we instead suffix the visible label with a hint and expose the
      // full evidence via the existing `data.evidence` field. The
      // accompanying test asserts the evidence is reachable through the
      // edge data so a future custom-edge component can render it inline.
      const evidence = isInferred ? (edge.evidence ?? '') : '';
      const baseLabel = isInferred
        ? evidence
          ? `inferred: ${truncateEvidence(evidence)}`
          : 'inferred'
        : edge.resolved
          ? 'resolved'
          : edge.sourceKnown
            ? 'blocking'
            : 'unknown';
      // critical-path-ordering Task 6 — an edge whose source AND target both
      // belong to the same dependency cycle (strongly-connected component or
      // self-edge) gets a distinct danger-coloured, solid stroke that
      // overrides the normal resolved/blocking/unknown palette AND the
      // dashed-inferred style, so a cycle reads as "stuck" regardless of its
      // origin. Members stay blocked — this is a read-only operator alert,
      // not a resolution mechanism (mirrors the Go-side doc comment on
      // DependencyCycleRow).
      const cycleMatch = findCycleForEdge(cycles, edge.sourceIdentifier, edge.targetIdentifier);
      const isCycleEdge = cycleMatch !== undefined;
      // unified-dependency-graph Task 8 — surface stale/overridden/gating as
      // a bracketed badge suffix on the label. These flags are only ever
      // non-false on inferred edges (see DependencyGraphEdgeRow doc
      // comments), so the guard is defensive rather than load-bearing. React
      // Flow's default edge type renders `label` as plain SVG text, so a
      // real styled badge component would require a custom edge type — kept
      // out of scope per the brief's "don't redesign the graph" note.
      const badgeParts: string[] = [];
      if (edge.gating) badgeParts.push('gating');
      if (edge.stale) badgeParts.push('stale');
      if (edge.overridden) badgeParts.push('overridden');
      if (isCycleEdge) badgeParts.push('cycle');
      const label = badgeParts.length > 0 ? `${baseLabel} [${badgeParts.join(', ')}]` : baseLabel;
      const hasEdgeData =
        edge.evidence !== undefined ||
        edge.stale !== undefined ||
        edge.overridden !== undefined ||
        edge.gating !== undefined;
      return {
        id: edge.id,
        source: edge.sourceIdentifier,
        target: edge.targetIdentifier,
        animated: !isInferred && !edge.resolved && edge.sourceKnown,
        label,
        labelStyle: isInferred ? { fontStyle: 'italic' } : undefined,
        data: hasEdgeData
          ? {
              evidence: edge.evidence,
              stale: edge.stale,
              overridden: edge.overridden,
              gating: edge.gating,
            }
          : undefined,
        style: {
          stroke: isCycleEdge
            ? 'var(--danger, #ef4444)'
            : edge.resolved
              ? 'var(--text-muted)'
              : edge.sourceKnown
                ? 'var(--accent)'
                : 'var(--warning, #f59e0b)',
          strokeWidth: isCycleEdge ? 2.5 : edge.resolved ? 1 : 2,
          strokeDasharray: isCycleEdge ? undefined : isInferred ? '4 2' : undefined,
          opacity: edge.resolved ? 0.55 : 0.9,
        },
      };
    }),
  };
}

export function DepsGraph({
  graphNodes,
  graphEdges,
  cycles,
  onSelectIssue,
  depsAnalyzerProfile,
  depsLastAnalyzedAt,
  depsAnalyzeJob,
  profileDefs,
}: {
  graphNodes: readonly DependencyGraphNode[];
  graphEdges: readonly DependencyGraphEdge[];
  // critical-path-ordering Task 6 — this tick's dependency-cycle rows,
  // sourced from the snapshot store (state.dependencyCycles) by the caller,
  // NOT recomputed client-side. Optional/absent renders no highlight, same
  // as older daemons that predate cycle detection.
  cycles?: readonly DependencyCycleRow[];
  onSelectIssue: (identifier: string) => void;
  depsAnalyzerProfile?: string;
  depsLastAnalyzedAt?: string;
  // #46-1 — the snapshot's current analyzer job (running or last-terminal),
  // sourced from the snapshot store (state.depsAnalyzeJob), NOT recomputed
  // client-side. Lets the toolbar below derive the Cancel control and the
  // passive "auto" badge from server state so a page refresh mid-run still
  // shows them, falling back to the click-driven mutation/poll state for
  // the freshest in-flight progress.
  depsAnalyzeJob?: DepsAnalyzeJob;
  profileDefs?: Record<string, ProfileDef>;
}) {
  // v0.2.0 audit P1-7 — memoise layout so it does not recompute on every
  // snapshot tick when nodes/edges did not change. Must run BEFORE the early
  // returns below to satisfy React's rules-of-hooks.
  const layout = useMemo(
    () => layoutDependencyGraph(graphNodes, graphEdges, cycles ?? []),
    [graphNodes, graphEdges, cycles],
  );

  // v0.2.0 audit P3-6 — accessible summary so screen-reader users and
  // colour-blind operators can read the unresolved/resolved counts without
  // relying on stroke colour alone.
  const ariaSummary = useMemo(() => {
    const blocking = graphEdges.filter((edge) => !edge.resolved).length;
    const resolved = graphEdges.length - blocking;
    return `Dependency graph: ${String(blocking)} blocking edge${blocking === 1 ? '' : 's'}, ${String(resolved)} resolved edge${resolved === 1 ? '' : 's'}`;
  }, [graphEdges]);

  // unified-dependency-graph Task 8 — track the last-clicked node locally so
  // the override control below can target it. This is a view-local selection
  // (not the app-wide issue-detail selection, which stays owned by
  // `onSelectIssue`/the dashboard) — clicking a node still opens issue detail
  // exactly as before; we additionally remember the identifier here.
  const [selectedIdentifier, setSelectedIdentifier] = useState<string | null>(null);
  const handleSelectIssue = (identifier: string) => {
    setSelectedIdentifier(identifier);
    onSelectIssue(identifier);
  };

  return (
    <div className="space-y-3">
      <DepsToolbar
        depsAnalyzerProfile={depsAnalyzerProfile}
        depsLastAnalyzedAt={depsLastAnalyzedAt}
        depsAnalyzeJob={depsAnalyzeJob}
        profileDefs={profileDefs}
      />
      <DepsCycleBanner cycles={cycles ?? []} />
      <DepsGraphCanvas
        graphNodes={graphNodes}
        graphEdges={graphEdges}
        onSelectIssue={handleSelectIssue}
        layout={layout}
        ariaSummary={ariaSummary}
      />
      <DepsOverridePanel selectedIdentifier={selectedIdentifier} graphEdges={graphEdges} />
    </div>
  );
}

/**
 * DepsCycleBanner — critical-path-ordering Task 6 review fix. The per-edge
 * red stroke + `[cycle]` badge (see layoutDependencyGraph) tells an operator
 * scanning the canvas THAT an edge belongs to a dependency cycle, but names
 * no members and carries no remediation guidance — this banner is the
 * actual visible surface for that. Lists every detected cycle (kind +
 * members), and for an `inferred`-kind cycle appends the analyzer-artifact
 * caveat from `describeCycle`. Hidden entirely when there are no cycles, so
 * a healthy graph stays uncluttered.
 */
function DepsCycleBanner({ cycles }: { cycles: readonly DependencyCycleRow[] }) {
  if (cycles.length === 0) return null;

  return (
    <div
      data-testid="deps-cycle-banner"
      className="border-theme-danger-soft bg-theme-danger-soft text-theme-danger flex flex-col gap-1.5 rounded-[var(--radius-md)] border px-3 py-2 text-xs"
    >
      {cycles.map((cycle, index) => (
        <div
          key={`${cycle.kind}:${cycle.members.join(',')}:${String(index)}`}
          className="flex flex-wrap items-start gap-1.5"
        >
          <span className="bg-theme-bg-elevated text-theme-text-secondary flex-shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium tracking-wide uppercase">
            {cycle.kind} cycle
          </span>
          <span>{describeCycle(cycle)}</span>
        </div>
      ))}
    </div>
  );
}

/**
 * unified-dependency-graph Task 8 — override control for the
 * selected issue's inferred (non-tracker) blockers. Renders nothing unless
 * the selected issue has at least one inferred edge that is currently gating
 * it or has already been overridden (so the "Restore" affordance stays
 * reachable after a dismiss flips `gating` back to false).
 */
function DepsOverridePanel({
  selectedIdentifier,
  graphEdges,
}: {
  selectedIdentifier: string | null;
  graphEdges: readonly DependencyGraphEdge[];
}) {
  const setDepsOverride = useSetDepsOverride();

  const inferredEdges = useMemo(
    () =>
      selectedIdentifier === null
        ? []
        : graphEdges.filter(
            (edge) => edge.targetIdentifier === selectedIdentifier && edge.origin === 'inferred',
          ),
    [graphEdges, selectedIdentifier],
  );

  if (selectedIdentifier === null || inferredEdges.length === 0) return null;

  const isOverridden = inferredEdges.some((edge) => edge.overridden);
  const isGating = inferredEdges.some((edge) => edge.gating);
  if (!isOverridden && !isGating) return null;

  return (
    <div
      data-testid="deps-override-panel"
      className="border-theme-line bg-theme-panel flex flex-wrap items-center justify-between gap-3 rounded-[var(--radius-md)] border px-3 py-2"
    >
      <div className="flex items-center gap-2">
        <span className="text-theme-text-secondary text-[11px]">
          Inferred blockers for <span className="font-mono">{selectedIdentifier}</span>:
        </span>
        <div className="flex flex-wrap gap-1">
          {isGating && <GraphBadge tone="accent">gating</GraphBadge>}
          {inferredEdges.some((edge) => edge.stale) && <GraphBadge tone="muted">stale</GraphBadge>}
          {isOverridden && <GraphBadge tone="success">overridden</GraphBadge>}
        </div>
      </div>
      <button
        type="button"
        disabled={setDepsOverride.isPending}
        onClick={() => {
          setDepsOverride.mutate({ identifier: selectedIdentifier, enabled: !isOverridden });
        }}
        title={
          isOverridden
            ? `Restore inferred blockers for ${selectedIdentifier}`
            : `Dismiss inferred blockers for ${selectedIdentifier}`
        }
        className="bg-theme-accent hover:bg-theme-accent-strong rounded-md px-3 py-1.5 text-xs font-semibold text-white shadow-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50"
        aria-label={isOverridden ? 'Restore inferred blockers' : 'Dismiss inferred blockers'}
      >
        {isOverridden ? 'Restore inferred blockers' : 'Dismiss inferred blockers'}
      </button>
    </div>
  );
}

function DepsGraphCanvas({
  graphNodes,
  graphEdges,
  onSelectIssue,
  layout,
  ariaSummary,
}: {
  graphNodes: readonly DependencyGraphNode[];
  graphEdges: readonly DependencyGraphEdge[];
  onSelectIssue: (identifier: string) => void;
  layout: { nodes: Node[]; edges: Edge[] };
  ariaSummary: string;
}) {
  // v0.2.0 audit P1-9 — distinguish the two genuinely-different empty states.
  // An empty edges set is the normal "no blockers configured" case; an empty
  // nodes set when edges exist is a server bug (the edge references nodes the
  // graph did not include) and gets a different message + console.warn.
  if (graphEdges.length === 0) {
    return (
      <div className="border-theme-line bg-theme-bg-soft text-theme-muted flex min-h-[280px] items-center justify-center rounded-[var(--radius-md)] border px-4 py-8 text-sm">
        No dependency edges found.
      </div>
    );
  }
  if (graphNodes.length === 0) {
    console.warn('DepsGraph: edges present but node metadata is empty — likely a server bug');
    return (
      <div className="border-theme-line bg-theme-bg-soft text-theme-warning flex min-h-[280px] items-center justify-center rounded-[var(--radius-md)] border px-4 py-8 text-sm">
        Dependency edges present but no node metadata — likely a server bug.
      </div>
    );
  }
  const handleNodeClick: NodeMouseHandler = (_event, node) => {
    const identifier = node.data.identifier;
    if (typeof identifier === 'string' && identifier !== '') onSelectIssue(identifier);
  };

  return (
    <div
      data-testid="deps-graph-canvas"
      role="img"
      aria-label={ariaSummary}
      className="border-theme-line bg-theme-bg-soft relative h-[520px] min-h-[320px] w-full overflow-hidden rounded-[var(--radius-md)] border md:h-[620px]"
    >
      <ReactFlow
        nodes={layout.nodes}
        edges={layout.edges}
        onNodeClick={handleNodeClick}
        fitView
        minZoom={0.3}
        maxZoom={1.6}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable
      >
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
      <DepsEdgeLegend />
    </div>
  );
}

function DepsToolbar({
  depsAnalyzerProfile,
  depsLastAnalyzedAt,
  depsAnalyzeJob,
  profileDefs,
}: {
  depsAnalyzerProfile?: string;
  depsLastAnalyzedAt?: string;
  depsAnalyzeJob?: DepsAnalyzeJob;
  profileDefs?: Record<string, ProfileDef>;
}) {
  const analyzeDeps = useAnalyzeDeps();
  const cancelDeps = useCancelAnalyzeDeps();

  // Addendum (cancel UI) — the mutation only resolves the final job at its
  // terminal status; the running job's ID and live chunk progress are
  // surfaced via the `onJobUpdate` callback threaded through deps.ts's
  // single existing poll loop (no second polling loop against the same
  // endpoint). `isMounted` guards the callback against firing after unmount
  // — the poll loop is not cancelled just because the component went away.
  const [liveJob, setLiveJob] = useState<DepsJobUpdate | null>(null);
  const isMounted = useRef(true);
  useEffect(() => {
    return () => {
      isMounted.current = false;
    };
  }, []);

  // gaps_11 G-14 / deps design §3.3 — operator-picked per-request override.
  // null means "follow the configured agent.deps_analyzer_profile default";
  // the backend accepts an explicit `profile` field on POST /deps/analyze
  // (handlers_deps.go) so no config change is needed to run a one-off pass
  // with a different profile.
  const [pickedProfile, setPickedProfile] = useState<string | null>(null);
  const selectedProfile = pickedProfile ?? depsAnalyzerProfile;

  // gaps_11 G-14 — the design asks for "profiles that can run dependency
  // analysis", but no `analyze_dependencies` capability exists in
  // AllowedAgentActionSchema, so no capability filter is derivable from the
  // snapshot. We list every configured profile, plus the configured analyzer
  // profile even when it is missing from profileDefs so the unknown-profile
  // state stays visible in the picker.
  const profileOptions = useMemo(() => {
    const names = new Set(Object.keys(profileDefs ?? {}));
    if (depsAnalyzerProfile) names.add(depsAnalyzerProfile);
    return Array.from(names).sort();
  }, [profileDefs, depsAnalyzerProfile]);

  const profileMissing = !selectedProfile;
  const profileDef = selectedProfile ? profileDefs?.[selectedProfile] : undefined;
  const profileDisabled = profileDef !== undefined && profileDef.enabled === false;
  const profileUnknown = selectedProfile !== undefined && profileDef === undefined;

  // #46-1 — the snapshot's current-job row is the server-derived source of
  // truth for "is a job running right now", independent of whether THIS
  // browser tab/click started it. It is what makes the Cancel control (and
  // the auto badge below) survive a page refresh: `liveJob` is mutation-
  // local state that resets to null on mount, so on a fresh page load
  // `analyzeDeps.isPending` is false and `liveJob` is null even while a job
  // is running server-side.
  const snapshotJobRunning =
    depsAnalyzeJob?.status === 'running' || depsAnalyzeJob?.status === 'queued';
  // effectiveJob merges the two sources with `liveJob` taking priority: once
  // a click starts a mutation, `liveJob` is populated immediately (with at
  // least a jobId) and updated on every poll tick, which is strictly
  // fresher progress than the once-per-snapshot-tick server row. Before any
  // click (or after remount/refresh) `liveJob` is null, so the snapshot row
  // is the only source — this is the fallback that makes a refreshed page
  // show the Cancel control immediately, without waiting for a click.
  const effectiveJob: DepsJobUpdate | null =
    liveJob ??
    (snapshotJobRunning
      ? {
          jobId: depsAnalyzeJob.jobId,
          status: depsAnalyzeJob.status,
          chunksTotal: depsAnalyzeJob.chunksTotal,
          chunksDone: depsAnalyzeJob.chunksDone,
          lastActivityAt: depsAnalyzeJob.lastActivityAt,
          trigger: depsAnalyzeJob.trigger,
        }
      : null);
  const isRunning = analyzeDeps.isPending || snapshotJobRunning;
  const buttonDisabled = profileMissing || profileDisabled || profileUnknown || isRunning;

  let disabledReason = '';
  if (profileMissing) {
    disabledReason = 'Set agent.deps_analyzer_profile in WORKFLOW.md to enable.';
  } else if (profileUnknown) {
    disabledReason = `Profile "${selectedProfile}" is not defined in agent.profiles.`;
  } else if (profileDisabled) {
    disabledReason = `Profile "${selectedProfile}" is disabled.`;
  } else if (isRunning) {
    disabledReason = 'Analysis pass already running.';
  }

  // Cancel only makes sense once we actually have a job ID to cancel — the
  // enqueue POST resolves fast, but isRunning can technically be true for
  // one tick before onJobUpdate's first call lands (and, on the snapshot
  // path, effectiveJob is derived straight from the row so this is
  // immediate).
  const showCancel = isRunning && effectiveJob !== null;

  // Suppress the chunk counter for a single-chunk run (chunksTotal === 1) —
  // rendering "chunk 1 / 1" is noise, not signal, since it can never advance.
  // `> 1` is the deliberate guard (not `> 0`): chunksTotal is `omitempty` on
  // the wire, so the server can never actually emit 0 — the reachable
  // "suppress" state is exactly chunksTotal === 1, which is also the default
  // (deps_analyzer_chunk_size: 75, so any backlog <= 75 issues is one
  // chunk). An absent chunksTotal (older daemon predating the field) is
  // caught by the same `?? 0` fallback.
  const showChunkProgress = isRunning && (effectiveJob?.chunksTotal ?? 0) > 1;
  // lastActivityAt is the only liveness signal on a single-chunk run, where
  // the chunk counter above is suppressed for the run's entire duration —
  // it must render independently of showChunkProgress, not only alongside
  // it.
  const showActivity = isRunning && effectiveJob?.lastActivityAt !== undefined;
  // analyzer-autonomy Task 5 / #52 passive-badge deferral — surface a small
  // "auto" badge next to the running-job status when the in-flight job was
  // scheduler-initiated (Task 4's periodic incremental analysis) rather than
  // started by this operator's own click. Deriving from effectiveJob (which
  // falls back to the snapshot row) makes this PASSIVE: it is visible on a
  // freshly loaded/refreshed page with no click required, closing the #52
  // deferral note ("visible only via the click-driven poll loop").
  const showAutoBadge = isRunning && effectiveJob?.trigger === 'auto';

  return (
    <div
      data-testid="deps-graph-toolbar"
      className="border-theme-line bg-theme-panel flex flex-wrap items-center justify-between gap-3 rounded-[var(--radius-md)] border px-3 py-2"
    >
      <div className="flex items-center gap-3">
        <button
          type="button"
          disabled={buttonDisabled}
          onClick={() => {
            setLiveJob(null);
            analyzeDeps.mutate({
              profile: selectedProfile,
              onJobUpdate: (update) => {
                if (isMounted.current) setLiveJob(update);
              },
            });
          }}
          title={disabledReason || `Run the ${selectedProfile ?? 'analyzer'} pass`}
          className="bg-theme-accent hover:bg-theme-accent-strong rounded-md px-3 py-1.5 text-xs font-semibold text-white shadow-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50"
          aria-label="Analyze dependencies"
        >
          {isRunning ? 'Analyzing…' : 'Analyze dependencies'}
        </button>
        {showAutoBadge && (
          <span
            data-testid="deps-analyze-trigger-auto"
            title="This analysis pass was started by the scheduler, not this click."
          >
            <GraphBadge tone="accent">auto</GraphBadge>
          </span>
        )}
        {showCancel && (
          <button
            type="button"
            disabled={cancelDeps.isPending}
            onClick={() => {
              // TS narrows `effectiveJob` to non-null here via control-flow
              // analysis of `showCancel` (the JSX guard above), which is
              // itself derived from `effectiveJob !== null`.
              cancelDeps.mutate({ jobId: effectiveJob.jobId });
            }}
            title="Stop the running analysis pass"
            className="bg-theme-danger rounded-md px-3 py-1.5 text-xs font-semibold text-white shadow-sm transition-colors hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
            aria-label="Cancel dependency analysis"
          >
            {cancelDeps.isPending ? 'Cancelling…' : 'Cancel'}
          </button>
        )}
        {(showChunkProgress || showActivity) && (
          <span
            className="text-theme-text-secondary text-[11px]"
            aria-live="polite"
            data-testid="deps-analyze-progress"
          >
            Analyzing…
            {showChunkProgress &&
              ` chunk ${String(effectiveJob?.chunksDone ?? 0)} / ${String(effectiveJob?.chunksTotal)}`}
            {showActivity && ` · last activity ${formatRelativeTime(effectiveJob.lastActivityAt)}`}
          </span>
        )}
        {selectedProfile !== undefined && (
          <label className="text-theme-text-secondary flex items-center gap-1.5 text-[11px]">
            Profile:
            <select
              data-testid="deps-profile-select"
              value={selectedProfile}
              onChange={(event) => {
                setPickedProfile(event.target.value);
              }}
              className="border-theme-line bg-theme-bg-soft text-theme-text rounded-md border px-1.5 py-0.5 font-mono text-[11px]"
            >
              {profileOptions.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>
      <span className="text-theme-text-secondary text-[11px]" aria-live="polite">
        Last analyzed: {formatRelativeTime(depsLastAnalyzedAt)}
      </span>
    </div>
  );
}

function DepsEdgeLegend() {
  return (
    <div className="border-theme-line bg-theme-bg-elevated text-theme-text-secondary absolute right-3 bottom-3 z-10 flex flex-col gap-1 rounded-md border px-2 py-1.5 text-[10px] leading-tight shadow-sm">
      <div className="flex items-center gap-1.5">
        <span className="bg-theme-accent inline-block h-0.5 w-6" aria-hidden />
        <span>tracker</span>
      </div>
      <div className="flex items-center gap-1.5">
        <span
          className="inline-block h-0.5 w-6 border-t-2 border-dashed"
          style={{ borderColor: 'var(--accent)' }}
          aria-hidden
        />
        <span>inferred</span>
      </div>
      {/* critical-path-ordering Task 6 */}
      <div className="flex items-center gap-1.5">
        <span
          className="inline-block h-[3px] w-6"
          style={{ background: 'var(--danger, #ef4444)' }}
          aria-hidden
        />
        <span>cycle</span>
      </div>
    </div>
  );
}

// truncateEvidence keeps the visible edge label readable on small graphs by
// capping long evidence strings to a single line. The full string remains
// accessible via the edge's `data.evidence` field for future hover-tooltip
// surfacing.
function truncateEvidence(s: string, max = 36): string {
  const trimmed = s.trim();
  if (trimmed.length <= max) return trimmed;
  return trimmed.slice(0, max - 1) + '…';
}

function formatRelativeTime(iso?: string): string {
  if (!iso) return 'Never';
  const time = new Date(iso).getTime();
  if (Number.isNaN(time)) return 'Never';
  const diffMs = Date.now() - time;
  if (diffMs < 0) return 'just now';
  const sec = Math.floor(diffMs / 1000);
  if (sec < 60) return `${String(sec)}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${String(min)}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${String(hr)}h ago`;
  const day = Math.floor(hr / 24);
  return `${String(day)}d ago`;
}

function IssueGraphLabel({ node }: { node: DependencyGraphNode }) {
  return (
    <div className="space-y-2 text-left">
      <div>
        <div className="text-theme-text font-mono text-[12px] font-semibold">{node.identifier}</div>
        {node.title && (
          <div className="text-theme-text-secondary mt-0.5 line-clamp-2 text-[11px] leading-snug">
            {node.title}
          </div>
        )}
      </div>
      <div className="flex flex-wrap gap-1">
        {node.state && <GraphBadge>{node.state}</GraphBadge>}
        {node.status && <GraphBadge>{node.status}</GraphBadge>}
        {node.running && <GraphBadge tone="success">running</GraphBadge>}
        {node.queued && <GraphBadge tone="accent">queued</GraphBadge>}
        {node.terminal && <GraphBadge tone="muted">terminal</GraphBadge>}
      </div>
    </div>
  );
}

function GraphBadge({
  children,
  tone = 'default',
}: {
  children: string;
  tone?: 'default' | 'success' | 'accent' | 'muted';
}) {
  const cls =
    tone === 'success'
      ? 'bg-theme-success-soft text-theme-success'
      : tone === 'accent'
        ? 'bg-theme-accent-soft text-theme-accent-strong'
        : tone === 'muted'
          ? 'bg-theme-bg-elevated text-theme-muted'
          : 'bg-theme-panel text-theme-text-secondary';
  return <span className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${cls}`}>{children}</span>;
}
