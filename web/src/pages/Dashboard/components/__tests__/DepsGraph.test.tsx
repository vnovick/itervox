import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { DepsGraph, layoutDependencyGraph } from '../DepsGraph';
import type {
  DependencyGraphEdge,
  DependencyGraphNode,
  ProfileDef,
} from '../../../../types/schemas';

function withQueryClient(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>;
}

vi.mock('@xyflow/react', () => ({
  ReactFlow: ({
    nodes,
    edges,
    onNodeClick,
  }: {
    nodes: Array<{ id: string; data: { label: React.ReactNode; identifier: string } }>;
    edges: Array<{ id: string; label?: string }>;
    onNodeClick?: (event: unknown, node: { data: { identifier: string } }) => void;
  }) => (
    <div data-testid="react-flow">
      {nodes.map((node) => (
        <button
          key={node.id}
          type="button"
          data-testid={`node-${node.id}`}
          onClick={() => onNodeClick?.({}, node)}
        >
          {node.data.label}
        </button>
      ))}
      {edges.map((edge) => (
        <span key={edge.id} data-testid={`edge-${edge.id}`}>
          {edge.label}
        </span>
      ))}
    </div>
  ),
  Background: () => <div data-testid="flow-background" />,
  Controls: () => <div data-testid="flow-controls" />,
}));

const graphNodes: DependencyGraphNode[] = [
  {
    id: 'DEMO-1',
    identifier: 'DEMO-1',
    title: 'Terminal blocker',
    state: 'Done',
    status: 'unblocked',
    running: false,
    queued: false,
    terminal: true,
  },
  {
    id: 'DEMO-2',
    identifier: 'DEMO-2',
    title: 'Blocked dependent',
    state: 'Backlog',
    status: 'blocked',
    running: true,
    queued: true,
    terminal: false,
  },
];

const graphEdges: DependencyGraphEdge[] = [
  {
    id: 'DEMO-1->DEMO-2',
    sourceIdentifier: 'DEMO-1',
    targetIdentifier: 'DEMO-2',
    sourceState: 'Done',
    targetState: 'Backlog',
    resolved: true,
    sourceKnown: true,
  },
];

describe('DepsGraph', () => {
  it('renders issue nodes, badges, and blocker edges', () => {
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
      ),
    );

    expect(screen.getByText('DEMO-1')).toBeInTheDocument();
    expect(screen.getByText('DEMO-2')).toBeInTheDocument();
    expect(screen.getByText('Done')).toBeInTheDocument();
    expect(screen.getByText('running')).toBeInTheDocument();
    expect(screen.getByText('queued')).toBeInTheDocument();
    expect(screen.getByTestId('edge-DEMO-1->DEMO-2')).toHaveTextContent('resolved');
  });

  it('selects the existing issue detail path when a node is clicked', () => {
    const onSelectIssue = vi.fn();
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={onSelectIssue} />,
      ),
    );

    fireEvent.click(screen.getByTestId('node-DEMO-2'));

    expect(onSelectIssue).toHaveBeenCalledWith('DEMO-2');
  });

  it('shows a compact empty state when there are no dependency edges', () => {
    render(withQueryClient(<DepsGraph graphNodes={[]} graphEdges={[]} onSelectIssue={vi.fn()} />));

    expect(screen.getByText('No dependency edges found.')).toBeInTheDocument();
  });

  it('uses a bounded mobile-safe canvas container', () => {
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
      ),
    );

    const canvas = screen.getByTestId('deps-graph-canvas');
    expect(canvas.className).toContain('w-full');
    expect(canvas.className).toContain('overflow-hidden');
    expect(canvas.className).not.toContain('overflow-x-auto');
  });

  it('renders unknown blockers visibly instead of dropping the edge', () => {
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={[
            {
              id: 'unknown:DEMO-4',
              identifier: 'unknown:DEMO-4',
              status: 'unknown',
              running: false,
              queued: false,
              terminal: false,
            },
            {
              id: 'DEMO-4',
              identifier: 'DEMO-4',
              status: 'unknown',
              running: false,
              queued: false,
              terminal: false,
            },
          ]}
          graphEdges={[
            {
              id: 'unknown:DEMO-4->DEMO-4',
              sourceIdentifier: 'unknown:DEMO-4',
              targetIdentifier: 'DEMO-4',
              resolved: false,
              sourceKnown: false,
            },
          ]}
          onSelectIssue={vi.fn()}
        />,
      ),
    );

    expect(screen.getByTestId('edge-unknown:DEMO-4->DEMO-4')).toHaveTextContent('unknown');
    expect(screen.getAllByText('unknown').length).toBeGreaterThan(0);
  });

  it('disables the Analyze button when no profile is configured', () => {
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
      ),
    );
    const button = screen.getByRole('button', { name: /analyze dependencies/i });
    expect(button).toBeDisabled();
    expect(button.getAttribute('title')).toMatch(/deps_analyzer_profile/);
  });

  it('disables the Analyze button when the configured profile is unknown', () => {
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={graphNodes}
          graphEdges={graphEdges}
          onSelectIssue={vi.fn()}
          depsAnalyzerProfile="missing-profile"
          profileDefs={{}}
        />,
      ),
    );
    const button = screen.getByRole('button', { name: /analyze dependencies/i });
    expect(button).toBeDisabled();
    expect(button.getAttribute('title')).toMatch(/not defined/);
  });

  it('disables the Analyze button when the configured profile is disabled', () => {
    const profileDef: ProfileDef = { command: 'claude', enabled: false };
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={graphNodes}
          graphEdges={graphEdges}
          onSelectIssue={vi.fn()}
          depsAnalyzerProfile="deps-analyzer"
          profileDefs={{ 'deps-analyzer': profileDef }}
        />,
      ),
    );
    const button = screen.getByRole('button', { name: /analyze dependencies/i });
    expect(button).toBeDisabled();
    expect(button.getAttribute('title')).toMatch(/disabled/i);
  });

  it('enables the Analyze button when the profile is set, present, and enabled', () => {
    const profileDef: ProfileDef = { command: 'claude', enabled: true };
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={graphNodes}
          graphEdges={graphEdges}
          onSelectIssue={vi.fn()}
          depsAnalyzerProfile="deps-analyzer"
          profileDefs={{ 'deps-analyzer': profileDef }}
        />,
      ),
    );
    const button = screen.getByRole('button', { name: /analyze dependencies/i });
    expect(button).not.toBeDisabled();
  });

  it('labels inferred edges with the inferred tag and dashed stroke', () => {
    const inferredEdge: DependencyGraphEdge = {
      id: 'A->B',
      sourceIdentifier: 'A',
      targetIdentifier: 'B',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
      evidence: 'title mentions depends on A',
    };
    const result = layoutDependencyGraph(
      [
        { id: 'A', identifier: 'A', running: false, queued: false, terminal: false },
        { id: 'B', identifier: 'B', running: false, queued: false, terminal: false },
      ],
      [inferredEdge],
    );
    const edge = result.edges.find((e) => e.id === 'A->B');
    // v0.2.0 todolist7 C5 — inferred edges now surface a snippet of the
    // evidence string in the label. The full string remains on edge.data.
    expect(edge?.label).toMatch(/^inferred: /);
    expect(edge?.label).toContain('title mentions depends on A');
    expect((edge?.style as { strokeDasharray?: string } | undefined)?.strokeDasharray).toBe('4 2');
    // Full evidence string is still reachable via edge.data for future custom-
    // edge components.
    expect((edge?.data as { evidence?: string } | undefined)?.evidence).toBe(
      'title mentions depends on A',
    );
  });

  // v0.2.0 todolist7 C5 — inferred edges with no evidence string fall back to
  // the bare 'inferred' label (the snippet is appended only when evidence
  // exists).
  it('falls back to bare "inferred" label when evidence is missing', () => {
    const inferredEdge: DependencyGraphEdge = {
      id: 'X->Y',
      sourceIdentifier: 'X',
      targetIdentifier: 'Y',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
    };
    const result = layoutDependencyGraph(
      [
        { id: 'X', identifier: 'X', running: false, queued: false, terminal: false },
        { id: 'Y', identifier: 'Y', running: false, queued: false, terminal: false },
      ],
      [inferredEdge],
    );
    const edge = result.edges.find((e) => e.id === 'X->Y');
    expect(edge?.label).toBe('inferred');
    expect((edge?.data as { evidence?: string } | undefined)?.evidence).toBeUndefined();
  });

  // v0.2.0 todolist7 C5 — long evidence strings are truncated so the visible
  // label stays readable on small graphs. The full string remains on
  // edge.data.evidence.
  it('truncates long evidence strings in the edge label', () => {
    const longEvidence = 'this is a very long evidence string that should be truncated for display';
    const inferredEdge: DependencyGraphEdge = {
      id: 'L->M',
      sourceIdentifier: 'L',
      targetIdentifier: 'M',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
      evidence: longEvidence,
    };
    const result = layoutDependencyGraph(
      [
        { id: 'L', identifier: 'L', running: false, queued: false, terminal: false },
        { id: 'M', identifier: 'M', running: false, queued: false, terminal: false },
      ],
      [inferredEdge],
    );
    const edge = result.edges.find((e) => e.id === 'L->M');
    expect(edge?.label).toMatch(/…$/);
    // Full string still accessible via data.
    expect((edge?.data as { evidence?: string } | undefined)?.evidence).toBe(longEvidence);
  });

  // v0.2.0 todolist7 C5 — in-canvas legend explains the dashed-vs-solid
  // convention so operators don't have to infer it from edge styling alone.
  it('renders the legend explaining tracker vs inferred edge styles', () => {
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
      ),
    );
    expect(screen.getByText('tracker')).toBeInTheDocument();
    expect(screen.getByText('inferred')).toBeInTheDocument();
  });

  it('shows the last-analyzed label when a timestamp is present', () => {
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={graphNodes}
          graphEdges={graphEdges}
          onSelectIssue={vi.fn()}
          depsLastAnalyzedAt={new Date(Date.now() - 60_000).toISOString()}
        />,
      ),
    );
    expect(screen.getByText(/Last analyzed/i)).toBeInTheDocument();
    // 60s ago should surface as "1m ago" (or similar relative time).
    expect(screen.getByText(/ago|just now/i)).toBeInTheDocument();
  });

  it('lays out blockers left of blocked issues', () => {
    const layout = layoutDependencyGraph(graphNodes, graphEdges);
    const blocker = layout.nodes.find((node) => node.id === 'DEMO-1');
    const blocked = layout.nodes.find((node) => node.id === 'DEMO-2');

    expect(blocker?.position.x).toBeLessThan(blocked?.position.x ?? 0);
  });
});
