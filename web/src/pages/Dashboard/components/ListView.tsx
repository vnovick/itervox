import { useState, useMemo } from 'react';
import Badge from '../../../components/ui/badge/Badge';
import { backendLabel, resolveIssueBackend } from '../../../lib/backends';
import type { TrackerIssue, ProfileDef } from '../../../types/schemas';
import { useCancelIssue, useResumeIssue } from '../../../queries/issues';
import { useItervoxStore } from '../../../store/itervoxStore';
import { EMPTY_STATES } from '../../../utils/constants';
import {
  orchStatusDotClass,
  stateBadgeColor,
  EMPTY_PROFILE_LABEL,
  formatOrchestratorState,
} from '../../../utils/format';
import { WhyIdleChip } from '../../../components/itervox/WhyIdleChip';
import { plainButtonClass } from '../../../components/ui/button/buttonRecipe';

const ROW_ACTION = plainButtonClass('secondary', 'xs', { pill: true });

type SortKey = 'identifier' | 'title' | 'state';
type SortDir = 'asc' | 'desc';

function SortIcon({ active, dir }: { active: boolean; dir: SortDir }) {
  return (
    <span className={`ml-1 ${active ? 'text-theme-accent-text' : 'text-theme-muted'}`}>
      {active ? (dir === 'asc' ? '↑' : '↓') : '↕'}
    </span>
  );
}

interface ListViewProps {
  issues: TrackerIssue[];
  onSelect: (id: string) => void;
  availableProfiles: string[];
  profileDefs?: Record<string, ProfileDef>;
  runningBackendByIdentifier?: Record<string, string>;
  defaultBackend?: string;
  backlogStates?: string[];
  onProfileChange: (identifier: string, profile: string) => void;
}

export function ListView({
  issues,
  onSelect,
  availableProfiles,
  profileDefs,
  runningBackendByIdentifier,
  defaultBackend,
  backlogStates,
  onProfileChange,
}: ListViewProps) {
  const backlogSet = useMemo(() => new Set(backlogStates ?? []), [backlogStates]);
  const [sortKey, setSortKey] = useState<SortKey>('identifier');
  const [sortDir, setSortDir] = useState<SortDir>('asc');
  const cancelIssueMutation = useCancelIssue();
  const resumeIssueMutation = useResumeIssue();

  // outbox #54 fast-follow: BoardView's DraggableCard has carried the
  // "⟳ Syncing" badge since Task 4 — ListView rows had no equivalent
  // marker (accepted-minor D on the final review). Same join-by-identifier
  // against snapshot.outboxSyncing as BoardView.
  const outboxSyncing = useItervoxStore((s) => s.snapshot?.outboxSyncing ?? EMPTY_STATES);
  const syncingIdentifiers = useMemo(() => new Set(outboxSyncing), [outboxSyncing]);

  const handleSort = (key: SortKey) => {
    if (sortKey === key) setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'));
    else {
      setSortKey(key);
      setSortDir('asc');
    }
  };

  const getVal = (issue: TrackerIssue, key: SortKey): string => {
    if (key === 'identifier') return issue.identifier;
    if (key === 'title') return issue.title.toLowerCase();
    return issue.state.toLowerCase();
  };

  const sorted = useMemo(
    () =>
      [...issues].sort((a, b) => {
        const cmp = getVal(a, sortKey).localeCompare(getVal(b, sortKey));
        return sortDir === 'asc' ? cmp : -cmp;
      }),
    [issues, sortKey, sortDir],
  );

  const thClass = 'select-none cursor-pointer';

  return (
    <div className="glass-card overflow-hidden">
      <div className="overflow-x-auto px-1.5">
        <table className="data-table min-w-[640px]">
          <thead>
            <tr>
              <th
                className={thClass}
                onClick={() => {
                  handleSort('identifier');
                }}
              >
                Identifier <SortIcon active={sortKey === 'identifier'} dir={sortDir} />
              </th>
              <th
                className={thClass}
                onClick={() => {
                  handleSort('title');
                }}
              >
                Title <SortIcon active={sortKey === 'title'} dir={sortDir} />
              </th>
              <th
                className={thClass}
                onClick={() => {
                  handleSort('state');
                }}
              >
                State <SortIcon active={sortKey === 'state'} dir={sortDir} />
              </th>
              <th className={thClass}>Backend</th>
              <th className={thClass}>Agent</th>
              <th className={thClass}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {sorted.length === 0 && (
              <tr>
                <td colSpan={6} className="text-theme-muted py-10 text-center">
                  No issues match the current filters
                </td>
              </tr>
            )}
            {sorted.map((issue) => (
              <tr
                key={issue.identifier}
                className="hover:bg-theme-bg-soft cursor-pointer transition-colors"
                onClick={() => {
                  onSelect(issue.identifier);
                }}
              >
                <td className="whitespace-nowrap">
                  {issue.url ? (
                    <a
                      href={issue.url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="text-theme-text font-mono text-xs font-medium hover:underline"
                      onClick={(e) => {
                        e.stopPropagation();
                      }}
                    >
                      {issue.identifier}
                    </a>
                  ) : (
                    <span className="text-theme-text font-mono text-xs font-medium">
                      {issue.identifier}
                    </span>
                  )}
                </td>
                <td className="max-w-xs">
                  <div className="flex min-w-0 items-center gap-2">
                    {/* CORE-068: the title is the row's keyboard entry point. */}
                    <button
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        onSelect(issue.identifier);
                      }}
                      className="text-theme-text min-w-0 cursor-pointer truncate rounded-sm text-left font-medium focus-visible:underline"
                    >
                      {issue.title}
                    </button>
                    {/* CORE-080 — why this idle issue is not dispatching. */}
                    <WhyIdleChip reason={issue.ineligibleReason} />
                    {blockerCount(issue) > 0 && (
                      <span
                        title={`Blocked by ${String(blockerCount(issue))} issue${blockerCount(issue) === 1 ? '' : 's'}`}
                        className="tag pill-warn flex-shrink-0"
                      >
                        Blocked {blockerCount(issue)}
                      </span>
                    )}
                  </div>
                </td>
                <td className="whitespace-nowrap">
                  <div className="flex items-center gap-1.5">
                    <Badge size="sm" color={stateBadgeColor(issue.state)}>
                      {issue.state}
                    </Badge>
                    {syncingIdentifiers.has(issue.identifier) && (
                      <span
                        data-testid="issue-row-syncing-badge"
                        title="A tracker state update for this issue is queued and not yet confirmed by the tracker"
                        className="tag pill-accent flex-shrink-0"
                      >
                        ⟳ Syncing
                      </span>
                    )}
                  </div>
                </td>
                <td className="whitespace-nowrap">
                  {(() => {
                    const b = resolveIssueBackend(
                      issue.agentProfile,
                      profileDefs,
                      runningBackendByIdentifier?.[issue.identifier],
                      defaultBackend,
                    );
                    return <span className="tag">{backendLabel(b)}</span>;
                  })()}
                </td>
                <td
                  className="whitespace-nowrap"
                  onClick={(e) => {
                    e.stopPropagation();
                  }}
                >
                  {(() => {
                    const isEditable = backlogSet.has(issue.state) && availableProfiles.length > 0;
                    if (isEditable) {
                      return (
                        <select
                          value={issue.agentProfile ?? ''}
                          onChange={(e) => {
                            onProfileChange(issue.identifier, e.target.value);
                          }}
                          aria-label={`Agent profile for ${issue.identifier}`}
                          className="glass-chip text-theme-text-secondary focus-visible:ring-theme-accent px-2.5 py-1 text-xs focus:outline-none focus-visible:ring-2"
                        >
                          <option value="">{EMPTY_PROFILE_LABEL}</option>
                          {availableProfiles.map((p) => (
                            <option key={p} value={p}>
                              {p}
                            </option>
                          ))}
                        </select>
                      );
                    }
                    return (
                      <span className="text-theme-text-secondary inline-flex items-center gap-1.5 text-xs">
                        <span
                          aria-hidden="true"
                          className={`status-dot ${orchStatusDotClass(issue.orchestratorState)}`}
                        />
                        {formatOrchestratorState(issue.orchestratorState)}
                        {issue.agentProfile && (
                          <span className="tag ml-1">{issue.agentProfile}</span>
                        )}
                      </span>
                    );
                  })()}
                </td>
                <td
                  className="whitespace-nowrap"
                  onClick={(e) => {
                    e.stopPropagation();
                  }}
                >
                  {issue.orchestratorState === 'running' && (
                    <button
                      onClick={() => {
                        cancelIssueMutation.mutate(issue.identifier);
                      }}
                      className={ROW_ACTION}
                    >
                      ⏸ Pause
                    </button>
                  )}
                  {issue.orchestratorState === 'paused' && (
                    <button
                      onClick={() => {
                        resumeIssueMutation.mutate(issue.identifier);
                      }}
                      className={ROW_ACTION}
                    >
                      ▶ Resume
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function blockerCount(issue: TrackerIssue): number {
  const detailCount = issue.blockedByDetails?.length ?? 0;
  if (detailCount > 0) return detailCount;
  return issue.blockedBy?.length ?? 0;
}
