import { fireEvent, render, screen } from '@testing-library/react';
import type React from 'react';
import { describe, expect, it, vi } from 'vitest';
import { DepsGraph, layoutDependencyGraph } from '../DepsGraph';
import type { DependencyGraphEdge, DependencyGraphNode } from '../../../../types/schemas';

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
    render(<DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />);

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
      <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={onSelectIssue} />,
    );

    fireEvent.click(screen.getByTestId('node-DEMO-2'));

    expect(onSelectIssue).toHaveBeenCalledWith('DEMO-2');
  });

  it('shows a compact empty state when there are no dependency edges', () => {
    render(<DepsGraph graphNodes={[]} graphEdges={[]} onSelectIssue={vi.fn()} />);

    expect(screen.getByText('No dependency edges found.')).toBeInTheDocument();
  });

  it('uses a bounded mobile-safe canvas container', () => {
    render(<DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />);

    const canvas = screen.getByTestId('deps-graph-canvas');
    expect(canvas.className).toContain('w-full');
    expect(canvas.className).toContain('overflow-hidden');
    expect(canvas.className).not.toContain('overflow-x-auto');
  });

  it('renders unknown blockers visibly instead of dropping the edge', () => {
    render(
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
    );

    expect(screen.getByTestId('edge-unknown:DEMO-4->DEMO-4')).toHaveTextContent('unknown');
    expect(screen.getAllByText('unknown').length).toBeGreaterThan(0);
  });

  it('lays out blockers left of blocked issues', () => {
    const layout = layoutDependencyGraph(graphNodes, graphEdges);
    const blocker = layout.nodes.find((node) => node.id === 'DEMO-1');
    const blocked = layout.nodes.find((node) => node.id === 'DEMO-2');

    expect(blocker?.position.x).toBeLessThan(blocked?.position.x ?? 0);
  });
});
