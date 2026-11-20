import { describe, expect, it } from 'vitest';
import type { LanesEdge } from '../lanes/lanesGraph';
import { buildLanesGraph, laneId, splitApplies, type FileNodeData } from '../lanes/lanesGraph';
import { StackSplitSchema } from '../../../../types/schemas';
import { codeFirstSplit, lanesResponse } from './lanesFixture';

/** The PR a file's edges lead to (through its column's junction). */
function prOf(edges: LanesEdge[], path: string): string | undefined {
  const toJunction = edges.find((e) => e.source === `file:${path}`);
  return edges.find((e) => e.source === toJunction?.target)?.target;
}

describe('buildLanesGraph (guardrails.lanes)', () => {
  it('puts each file in its lane column and every file into the one PR', () => {
    const { nodes, edges } = buildLanesGraph(lanesResponse({ approvalPending: true }), 'one');
    const headers = nodes.filter((n) => n.type === 'laneHeader').map((n) => n.data);
    expect(headers).toEqual([
      { kind: 'header', title: 'Auto', count: 2, lane: 'auto' },
      { kind: 'header', title: 'Second opinion', count: 1, lane: 'second_opinion' },
      { kind: 'header', title: 'Human', count: 1, lane: 'human' },
    ]);
    const files = nodes.filter((n) => n.type === 'laneFile');
    const xOf = (p: string) =>
      files.find((n) => (n.data as FileNodeData).file.path === p)?.position.x;
    expect(xOf('main.go')).toBe(0);
    expect(xOf('auth/login.go')).toBeGreaterThan(xOf('main.go') ?? 0);
    expect(xOf('.github/workflows/ci.yml')).toBeGreaterThan(xOf('auth/login.go') ?? 0);
    const prs = nodes.filter((n) => n.type === 'lanePR');
    expect(prs).toHaveLength(1);
    expect(prs[0].data).toMatchObject({ title: 'Pull request', lane: 'human', pending: true });
    for (const f of ['main.go', 'calc_test.go', 'auth/login.go', '.github/workflows/ci.yml']) {
      expect(prOf(edges, f)).toBe('pr');
    }
    expect(edges.find((e) => e.source === 'file:.github/workflows/ci.yml')?.animated).toBe(true);
    // every file edge runs into a junction under its own column, below every card
    const junctions = nodes.filter((n) => n.type === 'laneJunction');
    expect(junctions.map((n) => n.id).sort()).toEqual([
      'junction:auto:pr',
      'junction:human:pr',
      'junction:second_opinion:pr',
    ]);
    const lowestCard = Math.max(...files.map((n) => n.position.y));
    expect(junctions.every((j) => j.position.y > lowestCard)).toBe(true);
    // a file with findings takes more room: the next file in its column starts lower
    const autoYs = files.filter((n) => n.position.x === 0).map((n) => n.position.y);
    expect(autoYs[1] - autoYs[0]).toBe(74);
  });

  it('draws a code-first split as two stacked PRs, the code one first', () => {
    const data = lanesResponse({ split: StackSplitSchema.parse(codeFirstSplit) });
    expect(splitApplies(data.split)).toBe(true);
    const { nodes, edges } = buildLanesGraph(data, 'split');
    const prs = Object.fromEntries(nodes.filter((n) => n.type === 'lanePR').map((n) => [n.id, n]));
    expect(prs['pr-code'].data).toMatchObject({
      title: 'PR 1 · code · goes first',
      url: 'https://example.test/pr/7',
      lane: 'second_opinion',
    });
    expect(prs['pr-pipeline'].data).toMatchObject({
      title: 'PR 2 · pipeline · stacked',
      lane: 'human',
    });
    // the PR row sits under the columns, the one that goes first on the left
    expect(prs['pr-code'].position.x).toBeLessThan(prs['pr-pipeline'].position.x);
    expect(prs['pr-code'].position.y).toBe(prs['pr-pipeline'].position.y);
    const lowest = Math.max(...nodes.filter((n) => n.type === 'laneFile').map((n) => n.position.y));
    expect(prs['pr-code'].position.y).toBeGreaterThan(lowest);
    expect(prs['pr-code'].data).toMatchObject({
      detail: 'Against the base: the code passed its checks alone.',
    });
    expect(edges).toContainEqual(
      expect.objectContaining({
        source: 'pr-code',
        sourceHandle: 'stack-out',
        target: 'pr-pipeline',
        targetHandle: 'stack-in',
        label: 'stacked on',
      }),
    );
    expect(prOf(edges, '.github/workflows/ci.yml')).toBe('pr-pipeline');
    expect(prOf(edges, 'main.go')).toBe('pr-code');
  });

  it('draws a pipeline-first split with the pipeline PR first; combined is one PR', () => {
    const data = lanesResponse({
      split: StackSplitSchema.parse({ ...codeFirstSplit, order: 'pipeline_first' }),
    });
    const prs = buildLanesGraph(data, 'split').nodes.filter((n) => n.type === 'lanePR');
    expect(prs.find((n) => n.id === 'pr-pipeline')?.data).toMatchObject({
      title: 'PR 1 · pipeline · goes first',
      url: 'https://example.test/pr/7',
    });
    const combined = lanesResponse({
      split: StackSplitSchema.parse({ order: 'combined', reason: 'both failed alone' }),
    });
    expect(splitApplies(combined.split)).toBe(false);
    expect(
      buildLanesGraph(combined, 'split').nodes.filter((n) => n.type === 'lanePR'),
    ).toHaveLength(1);
  });

  it('ranks an unknown lane as human', () => {
    expect(laneId('weird')).toBe('human');
    expect(laneId(undefined)).toBe('auto');
    expect(laneId('second_opinion')).toBe('second_opinion');
  });
});
