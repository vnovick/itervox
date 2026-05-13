import { useState, useMemo } from 'react';
import Badge from '../../../components/ui/badge/Badge';
import type { TrackerIssue, ProfileDef } from '../../../types/schemas';
import { useCancelIssue, useResumeIssue } from '../../../queries/issues';
import {
  orchDotClass,
  stateBadgeColor,
  EMPTY_PROFILE_LABEL,
  formatOrchestratorState,
} from '../../../utils/format';

type SortKey = 'identifier' | 'title' | 'state';
type SortDir = 'asc' | 'desc';

function SortIcon({ active, dir }: { active: boolean; dir: SortDir }) {
  return (
    <span className="ml-1" style={{ color: active ? 'var(--accent)' : 'var(--muted)' }}>
      {active ? (dir === 'asc' ? '↑' : '↓') : '↕'}
    </span>
  );
}

function resolveBackend(
  profile: string | undefined,
  profileDefs: Record<string, ProfileDef> | undefined,
  runningBackend: string | undefined,
  defaultBackend: string | undefined,
): 'claude' | 'codex' {
  if (runningBackend) return /codex/i.test(runningBackend) ? 'codex' : 'claude';
  if (profile && profileDefs?.[profile]) {
    const def = profileDefs[profile];
    const hint = def.backend || def.command || '';
    if (hint) return /codex/i.test(hint) ? 'codex' : 'claude';
  }
  const fallback = defaultBackend || '';
  return /codex/i.test(fallback) ? 'codex' : 'claude';
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

  const thStyle: React.CSSProperties = { color: 'var(--text-secondary)' };
  const thClass =
    'px-4 py-3 text-left text-xs font-medium uppercase tracking-wider select-none cursor-pointer';

  return (
    <div className="border-theme-line bg-theme-panel overflow-hidden rounded-[var(--radius-md)] border">
      <div className="overflow-x-auto">
        <table className="w-full min-w-[640px] text-sm">
          <thead className="bg-theme-bg-soft">
            <tr>
              <th
                className={thClass}
                style={thStyle}
                onClick={() => {
                  handleSort('identifier');
                }}
              >
                Identifier <SortIcon active={sortKey === 'identifier'} dir={sortDir} />
              </th>
              <th
                className={thClass}
                style={thStyle}
                onClick={() => {
                  handleSort('title');
                }}
              >
                Title <SortIcon active={sortKey === 'title'} dir={sortDir} />
              </th>
              <th
                className={thClass}
                style={thStyle}
                onClick={() => {
                  handleSort('state');
                }}
              >
                State <SortIcon active={sortKey === 'state'} dir={sortDir} />
              </th>
              <th className={thClass} style={thStyle}>
                Backend
              </th>
              <th className={thClass} style={thStyle}>
                Agent
              </th>
              <th className={thClass} style={thStyle}>
                Actions
              </th>
            </tr>
          </thead>
          <tbody className="border-theme-line border-t">
            {sorted.length === 0 && (
              <tr>
                <td colSpan={6} className="text-theme-muted px-4 py-10 text-center text-sm">
                  No issues match the current filters
                </td>
              </tr>
            )}
            {sorted.map((issue) => (
              <tr
                key={issue.identifier}
                className="border-theme-line cursor-pointer border-t transition-colors hover:bg-[var(--bg-soft)]"
                onClick={() => {
                  onSelect(issue.identifier);
                }}
              >
                <td className="px-4 py-3 whitespace-nowrap">
                  {issue.url ? (
                    <a
                      href={issue.url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="text-theme-accent font-mono text-sm font-medium hover:underline"
                      onClick={(e) => {
                        e.stopPropagation();
                      }}
                    >
                      {issue.identifier}
                    </a>
                  ) : (
                    <span className="text-theme-text font-mono text-sm font-medium">
                      {issue.identifier}
                    </span>
                  )}
                </td>
                <td className="text-theme-text-secondary max-w-xs px-4 py-3">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate">{issue.title}</span>
                    {blockerCount(issue) > 0 && (
                      <span
                        title={`Blocked by ${String(blockerCount(issue))} issue${blockerCount(issue) === 1 ? '' : 's'}`}
                        className="bg-theme-danger-soft text-theme-danger flex-shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
                      >
                        Blocked {blockerCount(issue)}
                      </span>
                    )}
                  </div>
                </td>
                <td className="px-4 py-3 whitespace-nowrap">
                  <Badge size="sm" color={stateBadgeColor(issue.state)}>
                    {issue.state}
                  </Badge>
                </td>
                <td className="px-4 py-3 whitespace-nowrap">
                  {(() => {
                    const b = resolveBackend(
                      issue.agentProfile,
                      profileDefs,
                      runningBackendByIdentifier?.[issue.identifier],
                      defaultBackend,
                    );
                    return (
                      <span
                        className={`inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-semibold ${
                          b === 'codex'
                            ? 'bg-emerald-500/15 text-emerald-400'
                            : 'bg-orange-500/15 text-orange-400'
                        }`}
                      >
                        {b === 'codex' ? 'Codex' : 'Claude'}
                      </span>
                    );
                  })()}
                </td>
                <td
                  className="px-4 py-3 whitespace-nowrap"
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
                          className="border-theme-line bg-theme-bg-elevated text-theme-text-secondary rounded border px-1.5 py-0.5 text-xs focus:outline-none"
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
                      <span className="text-theme-muted inline-flex items-center gap-1 text-xs">
                        <span
                          className={`h-2 w-2 rounded-full ${orchDotClass(issue.orchestratorState)}`}
                        />
                        {formatOrchestratorState(issue.orchestratorState)}
                        {issue.agentProfile && (
                          <span className="border-theme-line ml-1 rounded border px-1 py-0.5 text-[10px]">
                            {issue.agentProfile}
                          </span>
                        )}
                      </span>
                    );
                  })()}
                </td>
                <td
                  className="px-4 py-3 whitespace-nowrap"
                  onClick={(e) => {
                    e.stopPropagation();
                  }}
                >
                  {issue.orchestratorState === 'running' && (
                    <button
                      onClick={() => {
                        cancelIssueMutation.mutate(issue.identifier);
                      }}
                      className="rounded px-2 py-1 text-xs transition-colors"
                      style={{
                        border: '1px solid var(--danger-soft)',
                        color: 'var(--danger)',
                        background: 'transparent',
                      }}
                    >
                      ⏸ Pause
                    </button>
                  )}
                  {issue.orchestratorState === 'paused' && (
                    <button
                      onClick={() => {
                        resumeIssueMutation.mutate(issue.identifier);
                      }}
                      className="rounded px-2 py-1 text-xs transition-colors"
                      style={{
                        border: '1px solid var(--success-soft)',
                        color: 'var(--success)',
                        background: 'transparent',
                      }}
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
