import { useMemo } from 'react';
import {
  Background,
  Controls,
  ReactFlow,
  type Edge,
  type Node,
  type NodeMouseHandler,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import type { DependencyGraphEdge, DependencyGraphNode } from '../../../types/schemas';

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
    edges: graphEdges.map((edge) => ({
      id: edge.id,
      source: edge.sourceIdentifier,
      target: edge.targetIdentifier,
      animated: !edge.resolved && edge.sourceKnown,
      label: edge.resolved ? 'resolved' : edge.sourceKnown ? 'blocking' : 'unknown',
      style: {
        stroke: edge.resolved
          ? 'var(--text-muted)'
          : edge.sourceKnown
            ? 'var(--accent)'
            : 'var(--warning, #f59e0b)',
        strokeWidth: edge.resolved ? 1 : 2,
        opacity: edge.resolved ? 0.55 : 0.9,
      },
    })),
  };
}

export function DepsGraph({
  graphNodes,
  graphEdges,
  onSelectIssue,
}: {
  graphNodes: readonly DependencyGraphNode[];
  graphEdges: readonly DependencyGraphEdge[];
  onSelectIssue: (identifier: string) => void;
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
    // eslint-disable-next-line no-console
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
      className="border-theme-line bg-theme-bg-soft h-[520px] min-h-[320px] w-full overflow-hidden rounded-[var(--radius-md)] border md:h-[620px]"
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
    </div>
  );
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
