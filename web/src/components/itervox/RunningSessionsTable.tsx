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
import { ConfirmButton } from '../ui/button/ConfirmButton';
import { EMPTY_RUNNING, EMPTY_PAUSED, EMPTY_PROFILES } from '../../utils/constants';
import { SessionAccordion } from './SessionAccordion';
import { AgentProfileSelector } from './selectors';
import { RunningRowMeta } from './RunningRowMeta';
import { useNow } from '../../hooks/useNow';
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
  // CORE-073 — Discard on both the running and the paused row (the spec's
  // verb: /terminate moves the issue to the first backlog state). Both
  // hit /terminate; separate instances keep each row's pending state and
  // success toast wording apart.
  const terminateIssueMutation = useTerminateIssue();
  const resumeIssueMutation = useResumeIssue();
  const setIssueProfileMutation = useSetIssueProfile();
  const { data: issues } = useIssues();

  const running = useStableRunning(rawRunning);
  const now = useNow(1_000); // CORE-090: one ticker for every row's age

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
    <div
      id="running-sessions"
      className="border-theme-line bg-theme-bg-elevated scroll-mt-20 overflow-hidden rounded-[var(--radius-md)] border"
    >
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
          <span className="bg-theme-success-soft text-theme-success-text inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-xs font-medium">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />
            {sorted.length} active
          </span>
        </div>
      )}

      {/* Running session rows */}
      {sorted.map((row) => (
        <div key={row.identifier} className="border-theme-line border-t">
          {/* Row — a mouse click anywhere toggles the log accordion; keyboard
              and assistive tech use the chevron button (M5-close: a
              role="button" row containing buttons was nested-interactive). */}
          {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions -- mouse shortcut only; the chevron button is the keyboard toggle (M5-close) */}
          <div
            data-testid={`running-row-${row.identifier}`}
            onClick={() => {
              toggle(row.identifier);
            }}
            // CORE-087 — a wrapping card on phones (the last event takes its
            // own line); the fixed seven-column grid from sm up.
            className="flex cursor-pointer flex-wrap items-center gap-x-3 gap-y-2 px-4 py-[14px] transition-colors select-none hover:bg-[var(--bg-soft)] sm:grid sm:grid-cols-[24px_100px_minmax(80px,auto)_56px_1fr_72px_auto] sm:gap-[14px]"
          >
            {/* Chevron — the accordion toggle */}
            <button
              type="button"
              aria-label={`Toggle details for ${row.identifier}`}
              aria-expanded={expandedId === row.identifier}
              onClick={(e) => {
                e.stopPropagation();
                toggle(row.identifier);
              }}
              className="text-theme-muted inline-flex h-6 w-6 items-center justify-center rounded text-[10px]"
            >
              <span
                aria-hidden="true"
                className="inline-block transition-transform duration-200"
                style={{ transform: expandedId === row.identifier ? 'rotate(90deg)' : 'none' }}
              >
                ▶
              </span>
            </button>

            {/* Identifier — opens the detail slide */}
            <button
              type="button"
              aria-label={`View details for ${row.identifier}`}
              className="text-theme-accent-text min-h-6 cursor-pointer truncate text-left font-mono text-sm font-semibold hover:underline"
              onClick={(e) => {
                e.stopPropagation();
                setSelectedIdentifier(row.identifier);
              }}
            >
              {row.identifier}
            </button>

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

            {/* Last event + CORE-090 backend/host/tokens/age/profile */}
            <span className="order-last flex min-w-0 basis-full flex-col gap-0.5 sm:order-none sm:basis-auto">
              <span
                className="text-theme-text-secondary truncate font-mono text-xs"
                title={row.lastEvent ?? undefined}
              >
                {row.lastEvent ? row.lastEvent.slice(0, 100) : '—'}
              </span>
              <RunningRowMeta row={row} profile={profileMap[row.identifier]} now={now} />
            </span>

            {/* Elapsed */}
            <span className="text-theme-muted font-mono text-xs">{fmtMs(row.elapsedMs)}</span>

            {/* Actions */}
            <div
              role="presentation"
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
                  color: 'var(--warning-text)',
                }}
              >
                ⏸ Pause
              </button>
              <ConfirmButton
                label="✕ Discard"
                confirmLabel="Yes, discard"
                pendingLabel="Discarding…"
                isPending={terminateIssueMutation.isPending}
                onConfirm={() => {
                  terminateIssueMutation.mutate(row.identifier);
                }}
              />
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
            <span className="text-theme-warning-text text-xs font-semibold tracking-[0.05em] uppercase">
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
                  {/* Paused row — a mouse click toggles the accordion; the
                      chevron button is the keyboard toggle (M5-close). */}
                  {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions -- mouse shortcut only; the chevron button is the keyboard toggle (M5-close) */}
                  <div
                    data-testid={`paused-row-${identifier}`}
                    onClick={() => {
                      togglePaused(identifier);
                    }}
                    className="flex cursor-pointer flex-wrap items-center gap-2 px-4 py-3 transition-colors hover:bg-[var(--bg-soft)]"
                  >
                    {/* Chevron — the accordion toggle */}
                    <button
                      type="button"
                      aria-label={`Toggle details for paused issue ${identifier}`}
                      aria-expanded={isExpanded}
                      onClick={(e) => {
                        e.stopPropagation();
                        togglePaused(identifier);
                      }}
                      className="text-theme-muted inline-flex h-6 w-6 items-center justify-center rounded text-[10px]"
                    >
                      <span
                        aria-hidden="true"
                        className="inline-block transition-transform duration-200"
                        style={{ transform: isExpanded ? 'rotate(90deg)' : 'none' }}
                      >
                        ▶
                      </span>
                    </button>

                    {/* Identifier — opens the detail slide */}
                    <button
                      type="button"
                      aria-label={`View details for paused issue ${identifier}`}
                      className="text-theme-warning-text min-h-6 cursor-pointer font-mono text-sm font-semibold hover:underline"
                      onClick={(e) => {
                        e.stopPropagation();
                        setSelectedIdentifier(identifier);
                      }}
                    >
                      {identifier}
                    </button>

                    {/* Title — truncated, fills remaining space */}
                    {issueTitle && (
                      <span className="text-theme-text-secondary hidden min-w-0 flex-1 truncate text-[13px] sm:inline">
                        {issueTitle}
                      </span>
                    )}

                    {/* PR badge */}
                    <div
                      role="presentation"
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
                          className="bg-theme-accent-soft text-theme-accent-text inline-flex flex-shrink-0 items-center rounded px-1.5 py-0.5 text-[10px] font-medium"
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
                      role="presentation"
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
                          color: 'var(--success-text)',
                        }}
                      >
                        ▶ Resume
                      </button>
                      <ConfirmButton
                        label="✕ Discard"
                        confirmLabel="Yes, discard"
                        pendingLabel="Discarding…"
                        isPending={terminateIssueMutation.isPending}
                        onConfirm={() => {
                          terminateIssueMutation.mutate(identifier);
                        }}
                      />
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
    </div>
  );
}
