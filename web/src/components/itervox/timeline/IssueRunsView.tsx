import { useMemo } from 'react';
import type { IssueLogEntry } from '../../../types/schemas';
import type { IssueGroup, NormalisedSession } from './types';
import { dedupSessionsPreferLive, extractSubagents } from './types';
import { RunRow } from './RunRow';

// Hook wrapper that computes per-run subagents via useMemo (hooks require a
// component boundary — they cannot be called inside a .map() callback).

interface RunRowWithSubagentsProps {
  run: NormalisedSession;
  logs: IssueLogEntry[];
  viewStart: number;
  viewEnd: number;
  expanded: boolean;
  selectedSubagentIdx: number | null;
  runNumber: number;
  onToggleExpand: () => void;
  onSelectSubagent: (idx: number | null) => void;
}

function RunRowWithSubagents({
  run,
  logs,
  viewStart,
  viewEnd,
  expanded,
  selectedSubagentIdx,
  runNumber,
  onToggleExpand,
  onSelectSubagent,
}: RunRowWithSubagentsProps) {
  const runSubagents = useMemo(() => extractSubagents(logs, run.sessionId), [logs, run.sessionId]);
  return (
    <RunRow
      session={run}
      subagents={runSubagents}
      viewStart={viewStart}
      viewEnd={viewEnd}
      expanded={expanded}
      selectedSubagentIdx={expanded ? selectedSubagentIdx : null}
      runNumber={runNumber}
      onToggleExpand={onToggleExpand}
      onSelectSubagent={onSelectSubagent}
    />
  );
}

interface IssueRunsViewProps {
  group: IssueGroup;
  logs: IssueLogEntry[];
  viewStart: number;
  viewEnd: number;
  expandedRunAt: string | null;
  selectedSubagentIdx: number | null;
  onToggleExpand: (runStartedAt: string) => void;
  onSelectSubagent: (idx: number | null) => void;
}

export function IssueRunsView({
  group,
  logs,
  viewStart,
  viewEnd,
  expandedRunAt,
  selectedSubagentIdx,
  onToggleExpand,
  onSelectSubagent,
}: IssueRunsViewProps) {
  // gaps_11 G-13(b) — drop live/history duplicates (same sessionId) before
  // rendering; without this the `run.sessionId` key below collides and the
  // same run renders twice during the stale-running window.
  const runs = useMemo(() => dedupSessionsPreferLive(group.runs), [group.runs]);
  return (
    <div className="mt-2 space-y-0.5">
      {runs.map((run, idx) => (
        <RunRowWithSubagents
          key={run.sessionId ?? `${run.identifier}-${run.startedAt}-${run.finishedAt ?? 'live'}`}
          run={run}
          logs={logs}
          viewStart={viewStart}
          viewEnd={viewEnd}
          expanded={expandedRunAt === run.startedAt}
          selectedSubagentIdx={selectedSubagentIdx}
          runNumber={idx + 1}
          onToggleExpand={() => {
            onToggleExpand(run.startedAt);
          }}
          onSelectSubagent={onSelectSubagent}
        />
      ))}
    </div>
  );
}
