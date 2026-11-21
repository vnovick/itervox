import { Suspense, lazy } from 'react';
import { useIssueLanes } from '../../../../queries/changes';
import type { StackSplit } from '../../../../types/schemas';
import { LaneReasonList } from '../../LaneReasonList';
import { LANE_TITLE, laneId, splitApplies } from './lanesGraph';

// The diagram pulls in @xyflow/react: load it only when the Lanes view
// opens, in its own chunk (the Deps graph does the same).
const LanesDiagram = lazy(() => import('./LanesDiagram'));

const ORDER_TEXT: Record<string, string> = {
  code_first:
    'The code goes first: it passed its checks alone; the pipeline change is stacked on it.',
  pipeline_first:
    'The pipeline change goes first: the code did not pass its checks without it; the code is stacked on it.',
};

function SplitSummary({ split }: { split: StackSplit }) {
  if (!splitApplies(split)) {
    return (
      <p className="text-theme-muted text-xs" data-testid="lanes-split">
        Not split: one pull request{split.reason ? ` (${split.reason})` : ''}.
      </p>
    );
  }
  return (
    <div className="text-theme-text-secondary space-y-0.5 text-xs" data-testid="lanes-split">
      <p className="text-theme-text font-semibold">Stack split</p>
      <p>{ORDER_TEXT[split.order]}</p>
      {split.reason && <p className="text-theme-muted">Why: {split.reason}</p>}
      <p className="text-theme-muted">
        Code: {split.codeFilesTotal || split.codeFiles.length} files · pipeline:{' '}
        {split.pipelineFilesTotal || split.pipelineFiles.length} files
      </p>
    </div>
  );
}

/**
 * guardrails.lanes — the Changes tab's Lanes view: the worktree's files in
 * their lanes (auto, second opinion, human) with the rule that put each
 * there, the findings on each file, the pull request(s) they flow into, and
 * why the change needs a person. Clicking a file opens its diff.
 */
export function LanesPanel({
  identifier,
  onOpenFile,
}: {
  identifier: string;
  onOpenFile: (path: string) => void;
}) {
  const { data, isLoading, error } = useIssueLanes(identifier, true);
  if (isLoading) return <p className="text-theme-muted text-sm">Loading lanes…</p>;
  if (error) return <p className="text-theme-danger-text text-sm">{error.message}</p>;
  if (!data) {
    return (
      <p className="text-theme-muted text-sm italic">No worktree for this issue — no lanes.</p>
    );
  }
  const lane = laneId(data.lane);
  return (
    <div className="space-y-3" data-testid="lanes-view">
      <p className="text-theme-muted text-xs">
        Against <code className="font-mono">{data.base}</code> · {data.files.length}{' '}
        {data.files.length === 1 ? 'file' : 'files'} · highest lane{' '}
        <span className="text-theme-text font-semibold">{LANE_TITLE[lane]}</span>
        {data.truncated ? ' (list truncated)' : ''}
        {data.findingsTruncated ? ' · findings incomplete' : ''}
      </p>
      {!data.enforced && (
        <p className="text-theme-warning-text text-xs">
          Guardrail lanes are off for this daemon: these lanes are what they would be.
        </p>
      )}
      {data.files.length === 0 ? (
        <p className="text-theme-muted text-sm italic">No changes against the base branch.</p>
      ) : (
        <Suspense
          fallback={
            <p role="status" className="text-theme-muted text-xs">
              Loading lanes diagram…
            </p>
          }
        >
          <LanesDiagram data={data} onOpenFile={onOpenFile} />
        </Suspense>
      )}
      <LaneReasonList reasons={data.reasons} total={data.reasonsTotal} />
      {data.split && <SplitSummary split={data.split} />}
      {!data.textAllowed && data.reasons.some((r) => r.kind === 'finding') && (
        <p className="text-theme-muted text-xs italic">
          The added lines of findings need an operator token; this token sees where they are.
        </p>
      )}
    </div>
  );
}
