import { Background, Controls, ReactFlow, type NodeMouseHandler } from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { useMemo, useState } from 'react';
import type { IssueLanesResponse } from '../../../../types/schemas';
import { buildLanesGraph, splitApplies, type LanesMode, type LanesNodeData } from './lanesGraph';
import { LaneFileNode, LaneHeaderNode, LaneJunctionNode, LanePRNode } from './LanesNodes';

const NODE_TYPES = {
  laneFile: LaneFileNode,
  lanePR: LanePRNode,
  laneHeader: LaneHeaderNode,
  laneJunction: LaneJunctionNode,
};

const MODES: readonly { id: LanesMode; label: string }[] = [
  { id: 'split', label: 'Split (two PRs)' },
  { id: 'one', label: 'One PR' },
];

/**
 * guardrails.lanes — the Lanes diagram (lazy: @xyflow/react loads only
 * when the Lanes view opens). Lane columns of file cards, findings on
 * their file, edges into the pull request — or the two stacked pull
 * requests of a split, with which goes first. Clicking a file card (its
 * name is a button) opens its diff.
 */
export default function LanesDiagram({
  data,
  onOpenFile,
}: {
  data: IssueLanesResponse;
  onOpenFile: (path: string) => void;
}) {
  const canSplit = splitApplies(data.split);
  const [mode, setMode] = useState<LanesMode>(canSplit ? 'split' : 'one');
  const shown: LanesMode = canSplit ? mode : 'one';
  const graph = useMemo(() => buildLanesGraph(data, shown), [data, shown]);
  const human = data.files.filter((f) => f.lane === 'human').length;
  const onNodeClick: NodeMouseHandler = (_e, node) => {
    const d = node.data as LanesNodeData;
    if (d.kind === 'file') onOpenFile(d.file.path);
  };

  return (
    <div className="space-y-2">
      {canSplit && (
        <div role="group" aria-label="Pull requests" className="segmented text-xs">
          {MODES.map((m) => (
            <button
              key={m.id}
              type="button"
              aria-pressed={shown === m.id}
              onClick={() => {
                setMode(m.id);
              }}
              className="px-2.5 py-0.5"
            >
              {m.label}
            </button>
          ))}
        </div>
      )}
      <div
        data-testid="lanes-diagram"
        role="group"
        aria-label={`${String(data.files.length)} changed files, ${String(human)} in the human lane, ${shown === 'split' ? 'split into two stacked pull requests' : 'one pull request'}`}
        className="glass-card relative h-[460px] w-full overflow-hidden"
      >
        <ReactFlow
          nodes={graph.nodes}
          edges={graph.edges}
          nodeTypes={NODE_TYPES}
          onNodeClick={onNodeClick}
          fitView
          minZoom={0.3}
          maxZoom={1.5}
          nodesDraggable={false}
          nodesConnectable={false}
        >
          <Background />
          <Controls showInteractive={false} />
        </ReactFlow>
      </div>
    </div>
  );
}
