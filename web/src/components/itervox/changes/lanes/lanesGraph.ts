import type { IssueLanesResponse, LaneFile, StackSplit } from '../../../../types/schemas';

// guardrails.lanes — the Lanes view's diagram as plain nodes and edges
// (positions included), so the layout is tested without React Flow.
// Columns left to right: auto, second opinion, human; under them the pull
// request(s) every file flows into. Each file's edge leaves its card on the
// right into the gutter beside its column and runs down to a junction under
// the column (one per column and pull request), which joins the pull
// request below — so no edge crosses another file's card.

export const LANE_ORDER = ['auto', 'second_opinion', 'human'] as const;
export type LaneId = (typeof LANE_ORDER)[number];

export const LANE_TITLE: Record<LaneId, string> = {
  auto: 'Auto',
  second_opinion: 'Second opinion',
  human: 'Human',
};

export type LanesMode = 'one' | 'split';

export interface FileNodeData {
  [key: string]: unknown;
  kind: 'file';
  file: LaneFile;
  lane: LaneId;
  /** Which pull request of a split the file goes to. */
  side?: 'code' | 'pipeline';
}

export interface PRNodeData {
  [key: string]: unknown;
  kind: 'pr';
  title: string;
  detail: string;
  lane: LaneId;
  url?: string;
  pending?: boolean;
}

export interface HeaderNodeData {
  [key: string]: unknown;
  kind: 'header';
  title: string;
  count: number;
  lane: LaneId;
}

export interface JunctionNodeData {
  [key: string]: unknown;
  kind: 'junction';
  lane: LaneId;
}

export type LanesNodeData = FileNodeData | PRNodeData | HeaderNodeData | JunctionNodeData;

export interface LanesNode {
  id: string;
  type: 'laneFile' | 'lanePR' | 'laneHeader' | 'laneJunction';
  position: { x: number; y: number };
  data: LanesNodeData;
  draggable?: boolean;
  selectable?: boolean;
}

export interface LanesEdge {
  id: string;
  source: string;
  sourceHandle?: string;
  target: string;
  targetHandle?: string;
  type?: 'smoothstep';
  label?: string;
  animated?: boolean;
  className?: string;
}

const COLUMN_X: Record<LaneId, number> = { auto: 0, second_opinion: 290, human: 580 };
const PR_ONE_X = 290;
const PR_LOWER_X = 120;
const PR_UPPER_X = 480;
const PR_GAP = 70;
const CARD_W = 240;
const JUNCTION_DX = [18, 32];
const JUNCTION_DY = 18;
const TOP = 56;
const FILE_GAP = 74;
const FINDING_ROW = 18;
const MAX_FINDINGS_SHOWN = 3;

/** A lane string from the daemon; anything unknown ranks as human. */
export function laneId(lane: string | undefined): LaneId {
  return lane === 'auto' || lane === 'second_opinion' ? lane : lane ? 'human' : 'auto';
}

function rank(l: LaneId): number {
  return LANE_ORDER.indexOf(l);
}

/** The split applies: two stacked pull requests (not "combined"). */
export function splitApplies(split: StackSplit | undefined): boolean {
  return split !== undefined && (split.order === 'code_first' || split.order === 'pipeline_first');
}

function laneOf(files: readonly LaneFile[]): LaneId {
  return files.reduce<LaneId>(
    (m, f) => (rank(laneId(f.lane)) > rank(m) ? laneId(f.lane) : m),
    'auto',
  );
}

function onePR(data: IssueLanesResponse): PRNodeData {
  const lane = laneId(data.lane);
  const detail: Record<LaneId, string> = {
    human: data.approvalPending
      ? 'Waiting for your approval. Itervox will not merge it.'
      : 'A person approves the push and merges the PR.',
    second_opinion: 'Merges after an approving review from another backend.',
    auto: 'Merges on green.',
  };
  return {
    kind: 'pr',
    title: 'Pull request',
    detail: detail[lane],
    lane,
    pending: data.approvalPending,
  };
}

function splitPRs(split: StackSplit, files: readonly LaneFile[]): [PRNodeData, PRNodeData] {
  const pipelineSet = new Set(split.pipelineFiles);
  const code = files.filter((f) => !pipelineSet.has(f.path));
  const pipeline = files.filter((f) => pipelineSet.has(f.path));
  const codeFirst = split.order === 'code_first';
  const codePR: PRNodeData = {
    kind: 'pr',
    title: codeFirst ? 'PR 1 · code · goes first' : 'PR 2 · code · stacked',
    detail: codeFirst ? againstBase(split.reason) : 'Stacked on the pipeline PR.',
    lane: laneId(split.codeLane ?? laneOf(code)),
    url: codeFirst ? split.lowerPrUrl : split.upperPrUrl,
  };
  const pipelinePR: PRNodeData = {
    kind: 'pr',
    title: codeFirst ? 'PR 2 · pipeline · stacked' : 'PR 1 · pipeline · goes first',
    detail: codeFirst
      ? 'Stacked on the code PR. A person approves and merges it.'
      : againstBase(split.reason),
    lane: pipeline.length > 0 ? laneOf(pipeline) : 'human',
    url: codeFirst ? split.upperPrUrl : split.lowerPrUrl,
  };
  return [codePR, pipelinePR];
}

function againstBase(reason: string | undefined): string {
  return reason ? `Against the base: ${reason}.` : 'Against the base.';
}

function fileHeight(f: LaneFile): number {
  return FILE_GAP + Math.min(f.findings.length, MAX_FINDINGS_SHOWN) * FINDING_ROW;
}

/** Nodes and edges of the Lanes diagram for `mode` ("split" needs a split). */
export function buildLanesGraph(
  data: IssueLanesResponse,
  mode: LanesMode,
): { nodes: LanesNode[]; edges: LanesEdge[] } {
  const nodes: LanesNode[] = [];
  const edges: LanesEdge[] = [];
  const split = mode === 'split' && splitApplies(data.split) ? data.split : undefined;
  const pipelineSet = new Set(split?.pipelineFiles ?? []);
  const fixed = { draggable: false, selectable: false };
  const prIds = split
    ? { code: 'pr-code', pipeline: 'pr-pipeline' }
    : { code: 'pr', pipeline: 'pr' };

  let bottom = TOP;
  const columns = LANE_ORDER.map((lane) => {
    const files = data.files.filter((f) => laneId(f.lane) === lane);
    nodes.push({
      id: `header-${lane}`,
      type: 'laneHeader',
      position: { x: COLUMN_X[lane], y: 0 },
      data: { kind: 'header', title: LANE_TITLE[lane], count: files.length, lane },
      ...fixed,
    });
    let y = TOP;
    const placed = files.map((f) => {
      const side: FileNodeData['side'] = split
        ? pipelineSet.has(f.path)
          ? 'pipeline'
          : 'code'
        : undefined;
      const node: LanesNode = {
        id: `file:${f.path}`,
        type: 'laneFile',
        position: { x: COLUMN_X[lane], y },
        data: { kind: 'file', file: f, lane, side },
        ...fixed,
      };
      nodes.push(node);
      y += fileHeight(f);
      return { id: node.id, target: side ? prIds[side] : 'pr' };
    });
    bottom = Math.max(bottom, y);
    return { lane, placed };
  });

  // Junctions under each column, one per pull request its files go to.
  for (const { lane, placed } of columns) {
    const targets = [...new Set(placed.map((p) => p.target))];
    targets.forEach((target, i) => {
      const jid = `junction:${lane}:${target}`;
      nodes.push({
        id: jid,
        type: 'laneJunction',
        position: { x: COLUMN_X[lane] + CARD_W + JUNCTION_DX[i % 2], y: bottom + JUNCTION_DY },
        data: { kind: 'junction', lane },
        ...fixed,
      });
      for (const p of placed.filter((q) => q.target === target)) {
        edges.push({
          id: `${p.id}->${jid}`,
          source: p.id,
          target: jid,
          type: 'smoothstep',
          animated: lane === 'human',
          className: `lanes-edge-${lane}`,
        });
      }
      edges.push({
        id: `${jid}->${target}`,
        source: jid,
        target,
        type: 'smoothstep',
        animated: lane === 'human',
        className: `lanes-edge-${lane}`,
      });
    });
  }

  const prY = bottom + PR_GAP;
  if (split) {
    const [codePR, pipelinePR] = splitPRs(split, data.files);
    const codeFirst = split.order === 'code_first';
    const [lower, upper] = codeFirst ? [prIds.code, prIds.pipeline] : [prIds.pipeline, prIds.code];
    const at = (id: string) => ({ x: id === lower ? PR_LOWER_X : PR_UPPER_X, y: prY });
    nodes.push(
      { id: prIds.code, type: 'lanePR', position: at(prIds.code), data: codePR, ...fixed },
      {
        id: prIds.pipeline,
        type: 'lanePR',
        position: at(prIds.pipeline),
        data: pipelinePR,
        ...fixed,
      },
    );
    edges.push({
      id: `${lower}->${upper}`,
      source: lower,
      sourceHandle: 'stack-out',
      target: upper,
      targetHandle: 'stack-in',
      label: 'stacked on',
      className: 'lanes-edge-stack',
    });
  } else {
    nodes.push({
      id: 'pr',
      type: 'lanePR',
      position: { x: PR_ONE_X, y: prY },
      data: onePR(data),
      ...fixed,
    });
  }
  return { nodes, edges };
}
