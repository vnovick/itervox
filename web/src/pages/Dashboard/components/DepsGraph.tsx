import { useMemo, useState } from 'react';
import {
  Background,
  Controls,
  ReactFlow,
  type Edge,
  type Node,
  type NodeMouseHandler,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { useAnalyzeDeps } from '../../../queries/deps';
import type { DependencyGraphEdge, DependencyGraphNode, ProfileDef } from '../../../types/schemas';

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
export function layoutDependencyGraph(
  graphNodes: readonly DependencyGraphNode[],
  graphEdges: readonly DependencyGraphEdge[],
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
      const label = isInferred
        ? evidence
          ? `inferred: ${truncateEvidence(evidence)}`
          : 'inferred'
        : edge.resolved
          ? 'resolved'
          : edge.sourceKnown
            ? 'blocking'
            : 'unknown';
      return {
        id: edge.id,
        source: edge.sourceIdentifier,
        target: edge.targetIdentifier,
        animated: !isInferred && !edge.resolved && edge.sourceKnown,
        label,
        labelStyle: isInferred ? { fontStyle: 'italic' } : undefined,
        data: edge.evidence ? { evidence: edge.evidence } : undefined,
        style: {
          stroke: edge.resolved
            ? 'var(--text-muted)'
            : edge.sourceKnown
              ? 'var(--accent)'
              : 'var(--warning, #f59e0b)',
          strokeWidth: edge.resolved ? 1 : 2,
          strokeDasharray: isInferred ? '4 2' : undefined,
          opacity: edge.resolved ? 0.55 : 0.9,
        },
      };
    }),
  };
}

export function DepsGraph({
  graphNodes,
  graphEdges,
  onSelectIssue,
  depsAnalyzerProfile,
  depsLastAnalyzedAt,
  profileDefs,
}: {
  graphNodes: readonly DependencyGraphNode[];
  graphEdges: readonly DependencyGraphEdge[];
  onSelectIssue: (identifier: string) => void;
  depsAnalyzerProfile?: string;
  depsLastAnalyzedAt?: string;
  profileDefs?: Record<string, ProfileDef>;
}) {
  // v0.2.0 audit P1-7 — memoise layout so it does not recompute on every
  // snapshot tick when nodes/edges did not change. Must run BEFORE the early
  // returns below to satisfy React's rules-of-hooks.
  const layout = useMemo(
    () => layoutDependencyGraph(graphNodes, graphEdges),
    [graphNodes, graphEdges],
  );

  // v0.2.0 audit P3-6 — accessible summary so screen-reader users and
  // colour-blind operators can read the unresolved/resolved counts without
  // relying on stroke colour alone.
  const ariaSummary = useMemo(() => {
    const blocking = graphEdges.filter((edge) => !edge.resolved).length;
    const resolved = graphEdges.length - blocking;
    return `Dependency graph: ${String(blocking)} blocking edge${blocking === 1 ? '' : 's'}, ${String(resolved)} resolved edge${resolved === 1 ? '' : 's'}`;
  }, [graphEdges]);

  return (
    <div className="space-y-3">
      <DepsToolbar
        depsAnalyzerProfile={depsAnalyzerProfile}
        depsLastAnalyzedAt={depsLastAnalyzedAt}
        profileDefs={profileDefs}
      />
      <DepsGraphCanvas
        graphNodes={graphNodes}
        graphEdges={graphEdges}
        onSelectIssue={onSelectIssue}
        layout={layout}
        ariaSummary={ariaSummary}
      />
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
  profileDefs,
}: {
  depsAnalyzerProfile?: string;
  depsLastAnalyzedAt?: string;
  profileDefs?: Record<string, ProfileDef>;
}) {
  const analyzeDeps = useAnalyzeDeps();

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
  const isRunning = analyzeDeps.isPending;
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
            analyzeDeps.mutate({ profile: selectedProfile });
          }}
          title={disabledReason || `Run the ${selectedProfile ?? 'analyzer'} pass`}
          className="bg-theme-accent hover:bg-theme-accent-strong rounded-md px-3 py-1.5 text-xs font-semibold text-white shadow-sm transition-colors disabled:cursor-not-allowed disabled:opacity-50"
          aria-label="Analyze dependencies"
        >
          {isRunning ? 'Analyzing…' : 'Analyze dependencies'}
        </button>
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
