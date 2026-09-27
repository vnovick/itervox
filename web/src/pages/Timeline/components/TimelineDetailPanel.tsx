import { AgentLogPanel } from '../../../components/itervox/timeline/AgentLogPanel';
import { TimeAxis } from '../../../components/itervox/timeline/TimeAxis';
import { IssueRunsView } from '../../../components/itervox/timeline/IssueRunsView';
import type {
  IssueGroup,
  NormalisedSession,
  SubagentSegment,
} from '../../../components/itervox/timeline/types';
import type { IssueLogEntry } from '../../../types/schemas';

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
  return (
    <div className="flex flex-1 flex-col overflow-hidden">
      <div className="border-theme-line flex flex-shrink-0 items-center justify-between border-b px-4 py-3">
        <div>
          <h2 className="text-theme-text text-base font-semibold">
            {selectedId ? `Timeline: ${selectedId}` : 'Timeline'}
          </h2>
          <p className="text-theme-text-secondary mt-1 text-[12px]">
            {anyLive
              ? 'Running \u00b7 click a run bar to expand subagents'
              : 'Completed sessions \u00b7 click a run to drill down'}
          </p>
          {currentAppSessionId && (
            <p className="text-theme-muted mt-0.5 font-mono text-[10px]">
              Session {currentAppSessionId.slice(0, 8)}
            </p>
          )}
        </div>
        {selectedGroup && (
          <span className="bg-theme-accent-soft text-theme-accent-strong inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-[12px] font-semibold">
            {selectedGroup.runs.some((r) => r.status === 'live') && (
              <span
                className="bg-theme-success inline-block animate-pulse rounded-full"
                style={{ width: 6, height: 6 }}
              />
            )}
            {selectedGroup.runs.length} run{selectedGroup.runs.length !== 1 ? 's' : ''}
          </span>
        )}
      </div>

      <div className="border-theme-line flex-1 overflow-y-auto border-b" style={{ padding: 16 }}>
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

      <div className="flex flex-1 flex-col overflow-hidden">
        {selectedId ? (
          <>
            <div className="border-theme-line bg-theme-panel-strong flex flex-shrink-0 items-center justify-between border-b px-3 py-2">
              <div className="flex items-center gap-2">
                <span className="text-[11px] font-bold tracking-[0.08em] uppercase">
                  Logs — {selectedId}
                </span>
                {!isSelectedLive && sublogs.length > 0 && (
                  <span
                    className="bg-theme-bg-soft text-theme-text-secondary rounded px-1.5 py-0.5 text-[9px] font-medium"
                    title="Full session logs from CLAUDE_CODE_LOG_DIR (includes all subagents)"
                  >
                    session logs \u00b7 {sublogs.length} events
                  </span>
                )}
                {validSubagentIdx !== null && subagentsForExpanded[validSubagentIdx] && (
                  <span className="bg-theme-accent-soft text-theme-accent-strong rounded px-1.5 py-0.5 text-[9px] font-medium">
                    {subagentsForExpanded[validSubagentIdx].name.slice(0, 8)}
                  </span>
                )}
              </div>
              <div className="flex items-center gap-2">
                {validSubagentIdx !== null && (
                  <button
                    onClick={() => {
                      onSelectSubagent(null);
                    }}
                    className="text-[10px] transition-opacity hover:opacity-70"
                    style={{
                      color: 'var(--muted)',
                      background: 'transparent',
                      border: '1px solid var(--line)',
                      padding: '4px 8px',
                      borderRadius: 4,
                      cursor: 'pointer',
                    }}
                  >
                    show all logs
                  </button>
                )}
                {selectedSession?.status === 'live' ? (
                  <span className="bg-theme-success-soft text-theme-success-text rounded px-1.5 py-0.5 text-[9px] font-medium">
                    live
                  </span>
                ) : (
                  selectedSession && (
                    <span
                      className="rounded px-1.5 py-0.5 text-[9px] font-medium"
                      style={
                        selectedSession.status === 'succeeded'
                          ? { background: 'var(--success-soft)', color: 'var(--success-text)' }
                          : { background: 'var(--danger-soft)', color: 'var(--danger-text)' }
                      }
                    >
                      {selectedSession.status}
                    </span>
                  )
                )}
              </div>
            </div>

            <div className="flex-1 overflow-hidden">
              {expandedRunAt == null ? (
                <div className="text-theme-muted bg-theme-panel-dark flex h-full items-center justify-center font-mono text-[12px] italic">
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
          </>
        ) : (
          <div className="text-theme-muted flex flex-1 items-center justify-center text-sm">
            Select an issue from the sidebar to view logs
          </div>
        )}
      </div>
    </div>
  );
}
