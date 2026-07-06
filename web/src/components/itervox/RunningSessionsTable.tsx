/* eslint-disable @typescript-eslint/no-unnecessary-condition */
import { useMemo, useCallback } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useStableRunning } from '../../hooks/useStableRunning';
import { useItervoxStore } from '../../store/itervoxStore';
import { useUIStore } from '../../store/uiStore';
import {
  useCancelIssue,
  useTerminateIssue,
  useResumeIssue,
  useSetIssueProfile,
  useIssues,
} from '../../queries/issues';
import { fmtMs, stateBadgeColor } from '../../utils/format';
import Badge from '../ui/badge/Badge';
import { EMPTY_RUNNING, EMPTY_PAUSED, EMPTY_PROFILES } from '../../utils/constants';
import { SessionAccordion } from './SessionAccordion';
import { AgentProfileSelector } from './selectors';
const EMPTY_PAUSED_WITH_PR: Record<string, string> = {};

export default function RunningSessionsTable() {
  const { rawRunning, paused, pausedWithPR, availableProfiles } = useItervoxStore(
    useShallow((s) => ({
      rawRunning: s.snapshot?.running ?? EMPTY_RUNNING,
      paused: s.snapshot?.paused ?? EMPTY_PAUSED,
      pausedWithPR: s.snapshot?.pausedWithPR ?? EMPTY_PAUSED_WITH_PR,
      availableProfiles: s.snapshot?.availableProfiles ?? EMPTY_PROFILES,
    })),
  );
  const setSelectedIdentifier = useItervoxStore((s) => s.setSelectedIdentifier);
  const cancelIssueMutation = useCancelIssue();
  const terminateIssueMutation = useTerminateIssue();
  const resumeIssueMutation = useResumeIssue();
  const setIssueProfileMutation = useSetIssueProfile();
  const { data: issues } = useIssues();

  const running = useStableRunning(rawRunning);

  const profileMap = useMemo(
    () =>
      Object.fromEntries(
        (issues ?? [])
          .filter((i): i is typeof i & { agentProfile: string } => Boolean(i.agentProfile))
          .map((i) => [i.identifier, i.agentProfile]),
      ),
    [issues],
  );
  const expandedId = useUIStore((s) => s.expandedRunningId);
  const setExpandedId = useUIStore((s) => s.setExpandedRunningId);
  const expandedPausedId = useUIStore((s) => s.expandedPausedId);
  const setExpandedPausedId = useUIStore((s) => s.setExpandedPausedId);

  const sorted = useMemo(
    () =>
      [...running].sort(
        (a, b) => new Date(a.startedAt).getTime() - new Date(b.startedAt).getTime(),
      ),
    [running],
  );

  const toggle = useCallback(
    (id: string) => {
      setExpandedId(expandedId === id ? null : id);
    },
    [expandedId, setExpandedId],
  );

  const togglePaused = useCallback(
    (id: string) => {
      setExpandedPausedId(expandedPausedId === id ? null : id);
    },
    [expandedPausedId, setExpandedPausedId],
  );

  if (sorted.length === 0 && paused.length === 0) {
    return (
      <div className="border-theme-line bg-theme-panel text-theme-muted rounded-[var(--radius-md)] border p-8 text-center text-sm">
        No agents running
      </div>
    );
  }

  return (
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      {/* Header — visible whenever there are running or paused sessions */}
      {sorted.length > 0 && (
        <div
          className="flex items-center justify-between px-4 py-[14px]"
          style={{
            borderBottom: paused.length > 0 ? '1px solid var(--line)' : undefined,
            borderColor: 'var(--line)',
          }}
        >
          <h3 className="text-theme-text text-[15px] font-semibold">Running Sessions</h3>
          <span className="bg-theme-success-soft text-theme-success inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-medium">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />
            {sorted.length} active
          </span>
        </div>
      )}

      {/* Running session rows */}
      {sorted.map((row) => (
        <div key={row.identifier} className="border-theme-line border-t">
          {/* Clickable row */}
          <div
            role="button"
            tabIndex={0}
            aria-label={`Toggle details for ${row.identifier}`}
            onClick={() => {
              toggle(row.identifier);
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') toggle(row.identifier);
            }}
            className="grid cursor-pointer items-center px-4 py-[14px] transition-colors select-none hover:bg-[var(--bg-soft)]"
            style={{
              gridTemplateColumns: '24px 100px minmax(80px, auto) 56px 1fr 72px auto',
              gap: '14px',
            }}
          >
            {/* Chevron */}
            <span
              className="text-[10px] transition-transform duration-200"
              style={{
                color: 'var(--muted)',
                transform: expandedId === row.identifier ? 'rotate(90deg)' : 'none',
                display: 'inline-block',
              }}
            >
              ▶
            </span>

            {/* Identifier — click opens detail slide */}
            <span
              role="button"
              tabIndex={0}
              aria-label={`View details for ${row.identifier}`}
              className="text-theme-accent cursor-pointer truncate font-mono text-sm font-semibold hover:underline"
              onClick={(e) => {
                e.stopPropagation();
                setSelectedIdentifier(row.identifier);
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.stopPropagation();
                  setSelectedIdentifier(row.identifier);
                }
              }}
            >
              {row.identifier}
            </span>

            {/* Kind + State badge */}
            <div className="flex items-center gap-1 whitespace-nowrap">
              {row.kind === 'reviewer' && (
                <span className="rounded bg-purple-500/15 px-1.5 py-0.5 text-[9px] font-semibold text-purple-400 uppercase">
                  Review
                </span>
              )}
              <Badge color={stateBadgeColor(row.state)} size="sm">
                {row.state}
              </Badge>
            </div>

            {/* Turn count + subagent badge */}
            <span className="text-theme-text-secondary flex items-center gap-1.5 text-sm">
              {row.turnCount ?? '\u2014'}
              {(row.subagentCount ?? 0) > 0 && (
                <span
                  className="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[9px] font-semibold"
                  style={{ background: 'var(--purple-soft)', color: 'var(--purple)' }}
                  title={`${String(row.subagentCount)} subagent${row.subagentCount === 1 ? '' : 's'}`}
                >
                  ↗ {row.subagentCount}
                </span>
              )}
            </span>

            {/* Last event */}
            <span
              className="text-theme-text-secondary truncate font-mono text-xs"
              title={row.lastEvent ?? undefined}
            >
              {row.lastEvent ? row.lastEvent.slice(0, 100) : '—'}
            </span>

            {/* Elapsed */}
            <span className="text-theme-muted font-mono text-xs">{fmtMs(row.elapsedMs)}</span>

            {/* Actions */}
            <div
              className="flex flex-shrink-0 gap-2"
              onClick={(e) => {
                e.stopPropagation();
              }}
            >
              <button
                onClick={() => {
                  cancelIssueMutation.mutate(row.identifier);
                }}
                className="inline-flex items-center rounded-[var(--radius-sm)] border px-3 py-1.5 text-xs font-medium transition-all"
                style={{
                  background: 'var(--warning-soft)',
                  borderColor: 'var(--warning-soft)',
                  color: 'var(--warning)',
                }}
              >
                ⏸ Pause
              </button>
              <button
                onClick={() => {
                  terminateIssueMutation.mutate(row.identifier);
                }}
                className="inline-flex items-center rounded-[var(--radius-sm)] border px-3 py-1.5 text-xs font-medium transition-all"
                style={{
                  background: 'var(--danger-soft)',
                  borderColor: 'var(--danger-soft)',
                  color: 'var(--danger)',
                }}
              >
                ✕ Cancel
              </button>
            </div>
          </div>

          {/* Expandable log accordion */}
          {expandedId === row.identifier && (
            <SessionAccordion
              identifier={row.identifier}
              workerHost={row.workerHost}
              sessionId={row.sessionId}
            />
          )}
        </div>
      ))}

      {/* Paused section — inside the same panel */}
      {paused.length > 0 && (
        <div
          style={{
            borderTop: sorted.length > 0 ? '1px solid var(--line)' : undefined,
            background: 'rgba(245,158,11,0.03)',
          }}
        >
          <div className="px-4 py-3">
            <span className="text-theme-warning text-xs font-semibold tracking-[0.05em] uppercase">
              ⏸ Paused ({paused.length})
            </span>
          </div>

          <div className="space-y-0 pb-3">
            {paused.map((identifier) => {
              const prURL = pausedWithPR[identifier];
              const issueTitle = issues?.find((i) => i.identifier === identifier)?.title;
              const isExpanded = expandedPausedId === identifier;
              return (
                <div key={identifier} className="border-theme-line border-b last:border-b-0">
                  {/* Paused row — click to expand accordion */}
                  <div
                    role="button"
                    tabIndex={0}
                    aria-label={`Toggle details for paused issue ${identifier}`}
                    onClick={() => {
                      togglePaused(identifier);
                    }}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') togglePaused(identifier);
                    }}
                    className="flex cursor-pointer flex-wrap items-center gap-2 px-4 py-3 transition-colors hover:bg-[var(--bg-soft)]"
                  >
                    {/* Chevron */}
                    <span
                      className="text-theme-muted text-[10px] transition-transform duration-200"
                      style={{ transform: isExpanded ? 'rotate(90deg)' : 'none' }}
                    >
                      ▶
                    </span>

                    {/* Identifier */}
                    <span
                      role="button"
                      tabIndex={0}
                      aria-label={`View details for paused issue ${identifier}`}
                      className="text-theme-warning cursor-pointer font-mono text-sm font-semibold hover:underline"
                      onClick={(e) => {
                        e.stopPropagation();
                        setSelectedIdentifier(identifier);
                      }}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.stopPropagation();
                          setSelectedIdentifier(identifier);
                        }
                      }}
                    >
                      {identifier}
                    </span>

                    {/* Title — truncated, fills remaining space */}
                    {issueTitle && (
                      <span className="text-theme-text-secondary hidden min-w-0 flex-1 truncate text-[13px] sm:inline">
                        {issueTitle}
                      </span>
                    )}

                    {/* PR badge */}
                    <div
                      className="ml-auto flex items-center gap-2"
                      onClick={(e) => {
                        e.stopPropagation();
                      }}
                    >
                      {prURL && (
                        <a
                          href={prURL}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="bg-theme-accent-soft text-theme-accent inline-flex flex-shrink-0 items-center rounded px-1.5 py-0.5 text-[10px] font-medium"
                          onClick={(e) => {
                            e.stopPropagation();
                          }}
                        >
                          PR
                        </a>
                      )}
                    </div>

                    {/* Actions — wrap on mobile */}
                    <div
                      className="mt-1 flex w-full flex-shrink-0 items-center gap-1.5 sm:mt-0 sm:ml-0 sm:w-auto"
                      onClick={(e) => {
                        e.stopPropagation();
                      }}
                    >
                      <AgentProfileSelector
                        value={profileMap[identifier] ?? ''}
                        availableProfiles={availableProfiles}
                        onChange={(profile) => {
                          setIssueProfileMutation.mutate({ identifier, profile });
                        }}
                      />
                      <button
                        onClick={() => {
                          resumeIssueMutation.mutate(identifier);
                        }}
                        className="btn-action-resume inline-flex items-center rounded-[var(--radius-sm)] border px-3 py-1.5 text-xs font-medium transition-all"
                        style={{
                          background: 'var(--success-soft)',
                          borderColor: 'var(--success-soft)',
                          color: 'var(--success)',
                        }}
                      >
                        ▶ Resume
                      </button>
                      <button
                        onClick={() => {
                          terminateIssueMutation.mutate(identifier);
                        }}
                        className="btn-action-cancel inline-flex items-center rounded-[var(--radius-sm)] border px-3 py-1.5 text-xs font-medium transition-all"
                        style={{
                          background: 'var(--danger-soft)',
                          borderColor: 'var(--danger-soft)',
                          color: 'var(--danger)',
                        }}
                      >
                        ✕ Discard
                      </button>
                    </div>
                  </div>

                  {/* Expandable accordion — reuses SessionAccordion */}
                  {isExpanded && (
                    <SessionAccordion
                      identifier={identifier}
                      workerHost={undefined}
                      sessionId={undefined}
                    />
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* Empty state */}
      {sorted.length === 0 && paused.length === 0 && (
        <div className="text-theme-muted px-4 py-8 text-center text-sm">No agents running</div>
      )}
    </div>
  );
}
