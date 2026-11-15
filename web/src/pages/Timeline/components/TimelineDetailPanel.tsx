import { AgentLogPanel } from '../../../components/itervox/timeline/AgentLogPanel';
import { TimeAxis } from '../../../components/itervox/timeline/TimeAxis';
import { IssueRunsView } from '../../../components/itervox/timeline/IssueRunsView';
import { RunProvenance } from '../../../components/itervox/timeline/RunProvenance';
import type {
  IssueGroup,
  NormalisedSession,
  SubagentSegment,
} from '../../../components/itervox/timeline/types';
import type { IssueLogEntry } from '../../../types/schemas';
import { Button } from '../../../components/ui/button';
import { ConsoleSurface } from '../../../components/ui/Console/ConsoleSurface';

interface TimelineDetailPanelProps {
  selectedId: string | null;
  selectedGroup: IssueGroup | null;
  currentAppSessionId?: string;
  anyLive: boolean;
  logsForSelected: IssueLogEntry[];
  viewStart: number;
  viewEnd: number;
  expandedRunAt: string | null;
  selectedSubagentIdx: number | null;
  onToggleExpand: (runStartedAt: string) => void;
  onSelectSubagent: (idx: number | null) => void;
  // Log panel
  expandedRun: NormalisedSession | null;
  selectedSession: NormalisedSession | null;
  isSelectedLive: boolean;
  sublogs: IssueLogEntry[];
  subagentsForExpanded: SubagentSegment[];
  validSubagentIdx: number | null;
  logPanelSlice: IssueLogEntry[];
}

export function TimelineDetailPanel({
  selectedId,
  selectedGroup,
  currentAppSessionId,
  anyLive,
  logsForSelected,
  viewStart,
  viewEnd,
  expandedRunAt,
  selectedSubagentIdx,
  onToggleExpand,
  onSelectSubagent,
  selectedSession,
  isSelectedLive,
  sublogs,
  subagentsForExpanded,
  validSubagentIdx,
  logPanelSlice,
}: TimelineDetailPanelProps) {
  const runCount = selectedGroup?.runs.length ?? 0;
  const groupLive = selectedGroup?.runs.some((r) => r.status === 'live') ?? false;
  return (
    <section
      aria-label="Run timeline"
      className="glass-panel flex max-h-full min-h-0 flex-col self-stretch overflow-hidden rounded-[26px] md:min-w-0 md:flex-1 md:self-start"
    >
      <div className="flex flex-shrink-0 flex-wrap items-start gap-3 px-5 pt-[18px] pb-3">
        <div className="min-w-0 flex-1">
          <h2 className="text-theme-text text-lg font-bold">
            {selectedId ? (
              <>
                Timeline: <span className="font-mono">{selectedId}</span>
              </>
            ) : (
              'Timeline'
            )}
          </h2>
          <p className="text-theme-text-secondary text-[13px]">
            {anyLive
              ? 'Running \u00b7 click a run bar to expand subagents'
              : 'Completed sessions \u00b7 click a run to drill down'}
            {currentAppSessionId && (
              <span className="font-mono"> · Session {currentAppSessionId.slice(0, 8)}</span>
            )}
          </p>
        </div>
        {selectedGroup && (
          <span className="pill pill-accent">
            {groupLive && <span aria-hidden="true" className="status-dot dot-ok animate-pulse" />}
            {runCount} run{runCount !== 1 ? 's' : ''}
          </span>
        )}
      </div>

      <div className="@container max-h-[45dvh] min-h-0 shrink-0 overflow-y-auto px-5 pb-3">
        {!selectedGroup ? (
          <div className="text-theme-muted flex h-12 items-center justify-center text-sm">
            Select an issue from the sidebar
          </div>
        ) : (
          <>
            <TimeAxis viewStart={viewStart} viewEnd={viewEnd} />
            <IssueRunsView
              group={selectedGroup}
              logs={logsForSelected}
              viewStart={viewStart}
              viewEnd={viewEnd}
              expandedRunAt={expandedRunAt}
              selectedSubagentIdx={selectedSubagentIdx}
              onToggleExpand={onToggleExpand}
              onSelectSubagent={onSelectSubagent}
            />
          </>
        )}
      </div>

      <div className="flex min-h-0 flex-col gap-3 px-5 pb-5">
        {selectedId ? (
          <>
            {selectedSession && selectedSession.status !== 'live' && (
              <RunProvenance run={selectedSession} />
            )}

            <ConsoleSurface
              data-testid="timeline-log-console"
              className="flex min-h-0 flex-col rounded-[18px]"
            >
              <div className="console-bar flex flex-shrink-0 flex-wrap items-center justify-between gap-2 px-4 py-2.5">
                <div className="text-theme-muted flex flex-wrap items-center gap-1.5 font-mono text-xs">
                  <span>Logs — {selectedId}</span>
                  {!isSelectedLive && sublogs.length > 0 && (
                    <span title="Full session logs from CLAUDE_CODE_LOG_DIR (includes all subagents)">
                      · session logs · {sublogs.length} events
                    </span>
                  )}
                  {validSubagentIdx !== null && subagentsForExpanded[validSubagentIdx] && (
                    <span className="pill pill-accent px-2 py-0 text-[11px]">
                      {subagentsForExpanded[validSubagentIdx].name.slice(0, 8)}
                    </span>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  {validSubagentIdx !== null && (
                    <Button
                      variant="glass"
                      size="xs"
                      pill
                      onClick={() => {
                        onSelectSubagent(null);
                      }}
                    >
                      show all logs
                    </Button>
                  )}
                  {selectedSession?.status === 'live' ? (
                    <span className="pill pill-ok px-2 py-0 text-[11px]">live</span>
                  ) : (
                    selectedSession && (
                      <span
                        className={`pill px-2 py-0 text-[11px] ${
                          selectedSession.status === 'succeeded' ? 'pill-ok' : 'pill-danger'
                        }`}
                      >
                        {selectedSession.status}
                      </span>
                    )
                  )}
                </div>
              </div>

              <div className="flex min-h-0 flex-col overflow-hidden">
                {expandedRunAt == null ? (
                  <div className="text-theme-muted flex min-h-24 items-center justify-center font-mono text-xs italic">
                    Select a run to show logs per run
                  </div>
                ) : (
                  <AgentLogPanel
                    key={expandedRunAt}
                    identifier={selectedId}
                    logSlice={logPanelSlice}
                  />
                )}
              </div>
            </ConsoleSurface>
          </>
        ) : (
          <div className="text-theme-muted flex flex-1 items-center justify-center text-sm">
            Select an issue from the sidebar to view logs
          </div>
        )}
      </div>
    </section>
  );
}
