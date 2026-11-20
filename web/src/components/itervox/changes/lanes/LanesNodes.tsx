import { Handle, Position, type NodeProps } from '@xyflow/react';
import { findingSeverity, laneReasonKey } from '../../../../utils/laneReasons';
import type {
  FileNodeData,
  HeaderNodeData,
  JunctionNodeData,
  LaneId,
  PRNodeData,
} from './lanesGraph';

// guardrails.lanes — the Lanes diagram's custom nodes. Only LanesDiagram
// (a lazy chunk) imports this file, so @xyflow/react stays out of the
// Changes tab's own chunk. A file card's name is a button (focusable,
// Enter/Space click it); the click reaches LanesDiagram's onNodeClick,
// which opens the file — React Flow only lets pointer events through to a
// node when the flow has a node click handler.

const LANE_BORDER: Record<LaneId, string> = {
  auto: 'border-theme-line',
  second_opinion: 'border-theme-accent',
  human: 'border-theme-warning',
};

const FINDINGS_SHOWN = 3;

export function LaneHeaderNode({ data }: NodeProps) {
  const d = data as HeaderNodeData;
  return (
    <div className="text-theme-text w-60 text-xs font-semibold tracking-wide uppercase">
      {d.title} <span className="text-theme-muted font-normal">({d.count})</span>
    </div>
  );
}

export function LaneFileNode({ data }: NodeProps) {
  const d = data as FileNodeData;
  const f = d.file;
  const more = f.findings.length - FINDINGS_SHOWN;
  return (
    <div
      className={`bg-theme-bg-elevated w-60 rounded-lg border px-2 py-1.5 text-xs shadow-sm ${LANE_BORDER[d.lane]}`}
      data-testid={`lane-file-${f.path}`}
    >
      <button
        type="button"
        className="text-theme-text hover:text-theme-accent-text focus-visible:ring-theme-accent block w-full truncate text-left font-mono focus-visible:ring-1"
        title={`Open the diff of ${f.path}`}
      >
        {f.path}
      </button>
      <p className="text-theme-muted truncate font-mono text-[11px]">
        {f.rule ? `matched ${f.rule}` : 'no lane rule'} · +{f.additions} −{f.deletions}
        {d.side ? ` · ${d.side} PR` : ''}
      </p>
      {f.findings.slice(0, FINDINGS_SHOWN).map((r) => (
        <p
          key={laneReasonKey(r)}
          className={`truncate text-[11px] ${findingSeverity(r) === 'high' ? 'text-theme-danger-text' : 'text-theme-warning-text'}`}
          title={r.text ? `${r.message ?? ''}: ${r.text}` : r.message}
        >
          :{r.line ?? '–'} {r.text ? r.text : r.message}
        </p>
      ))}
      {more > 0 && <p className="text-theme-muted text-[11px]">+{more} more findings</p>}
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  );
}

export function LanePRNode({ data }: NodeProps) {
  const d = data as PRNodeData;
  return (
    <div
      className={`bg-theme-bg-soft w-64 rounded-lg border-2 px-3 py-2 text-xs ${LANE_BORDER[d.lane]}`}
      data-testid="lane-pr"
    >
      <Handle type="target" position={Position.Top} isConnectable={false} />
      <p className="text-theme-text font-semibold">
        {d.url ? (
          <a href={d.url} target="_blank" rel="noreferrer" className="hover:underline">
            {d.title}
          </a>
        ) : (
          d.title
        )}
      </p>
      <p className={d.pending ? 'text-theme-warning-text' : 'text-theme-text-secondary'}>
        {d.detail}
      </p>
      <Handle id="stack-out" type="source" position={Position.Right} isConnectable={false} />
      <Handle id="stack-in" type="target" position={Position.Left} isConnectable={false} />
    </div>
  );
}

/** Where a column's edges join before they go into a pull request. */
export function LaneJunctionNode({ data }: NodeProps) {
  const d = data as JunctionNodeData;
  return (
    <div className={`h-1.5 w-1.5 rounded-full border ${LANE_BORDER[d.lane]}`} aria-hidden="true">
      <Handle type="target" position={Position.Top} isConnectable={false} />
      <Handle type="source" position={Position.Bottom} isConnectable={false} />
    </div>
  );
}
