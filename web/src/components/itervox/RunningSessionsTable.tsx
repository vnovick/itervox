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
import { plainButtonClass } from '../ui/button/buttonRecipe';

const ACTION_BUTTON = plainButtonClass('secondary', 'sm', { pill: true });
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
      <div className="glass-panel text-theme-muted p-8 text-center text-sm">No agents running</div>
    );
  }

  return (
    <section
      id="running-sessions"
      aria-labelledby={sorted.length > 0 ? 'running-sessions-heading' : undefined}
      className="glass-panel flex scroll-mt-20 flex-col gap-2 px-3 py-4 sm:px-[18px]"
    >
      {/* Header — visible whenever there are running sessions */}
      {sorted.length > 0 && (
        <div className="flex items-center gap-2 px-1">
          <h3 id="running-sessions-heading" className="section-title">
            Running Sessions
          </h3>
          <span className="text-theme-text-secondary inline-flex items-center gap-1.5 text-xs">
            <span aria-hidden="true" className="status-dot dot-ok h-1.5 w-1.5 animate-pulse" />
            {sorted.length} active
          </span>
        </div>
      )}

      {/* Running session rows — one card each */}
      {sorted.map((row) => (
        <div key={row.identifier} className="glass-card overflow-hidden">
          {/* Row — a mouse click anywhere toggles the log accordion; keyboard
              and assistive tech use the chevron button (M5-close: a
              role="button" row containing buttons was nested-interactive). */}
          {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions -- mouse shortcut only; the chevron button is the keyboard toggle (M5-close) */}
          <div
            data-testid={`running-row-${row.identifier}`}
            onClick={() => {
              toggle(row.identifier);
            }}
            // CORE-087 — one wrapping card at every width: the header line
            // (id, state, turn, actions) wraps on phones, and the last event
            // and its meta line always take full rows below it.
            className="flex cursor-pointer flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2.5 select-none"
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
              className="text-theme-text-secondary hover:bg-theme-bg-soft inline-flex h-6 w-6 items-center justify-center rounded-full text-sm"
            >
              <span
                aria-hidden="true"
                className={`inline-block transition-transform duration-200 ${
                  expandedId === row.identifier ? 'rotate-90' : ''
                }`}
              >
                ›
              </span>
            </button>

            {/* Identifier — opens the detail slide */}
            <button
              type="button"
              aria-label={`View details for ${row.identifier}`}
              className="text-theme-text min-h-6 cursor-pointer truncate text-left font-mono text-xs font-semibold hover:underline"
              onClick={(e) => {
                e.stopPropagation();
                setSelectedIdentifier(row.identifier);
              }}
            >
              {row.identifier}
            </button>

            {/* Kind + State badge */}
            <div className="flex items-center gap-1 whitespace-nowrap">
              {row.kind === 'reviewer' && <span className="tag pill-accent">Review</span>}
              <Badge color={stateBadgeColor(row.state)} size="sm">
                {row.state}
              </Badge>
            </div>

            {/* Turn count + subagent badge */}
            <span className="text-theme-muted flex items-center gap-1.5 text-[13px]">
              {row.turnCount != null ? `turn ${String(row.turnCount)}` : '—'}
              {(row.subagentCount ?? 0) > 0 && (
                <span
                  className="tag bg-theme-purple-soft text-theme-purple-text"
                  title={`${String(row.subagentCount)} subagent${row.subagentCount === 1 ? '' : 's'}`}
                >
                  ↗ {row.subagentCount}
                </span>
              )}
            </span>

            {/* Elapsed */}
            <span className="text-theme-muted font-mono text-xs">{fmtMs(row.elapsedMs)}</span>

            {/* Actions */}
            <div
              role="presentation"
              className="ml-auto flex flex-shrink-0 gap-1.5"
              onClick={(e) => {
                e.stopPropagation();
              }}
            >
              <button
                type="button"
                onClick={() => {
                  cancelIssueMutation.mutate(row.identifier);
                }}
                className={ACTION_BUTTON}
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

            {/* Last event + CORE-090 backend/host/tokens/age/profile */}
            <span className="flex min-w-0 basis-full flex-col gap-0.5 pl-8">
              <span
                className="text-theme-text-secondary truncate text-[13px]"
                title={row.lastEvent ?? undefined}
              >
                {row.lastEvent ? row.lastEvent.slice(0, 100) : '—'}
              </span>
              <RunningRowMeta row={row} profile={profileMap[row.identifier]} now={now} />
            </span>
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
        <div className={`flex flex-col gap-2 ${sorted.length > 0 ? 'pt-2' : ''}`}>
          <h4 className="text-theme-warning-text px-1 text-xs font-semibold">
            ⏸ Paused ({paused.length})
          </h4>

          {paused.map((identifier) => {
            const prURL = pausedWithPR[identifier];
            const issueTitle = issues?.find((i) => i.identifier === identifier)?.title;
            const isExpanded = expandedPausedId === identifier;
            return (
              <div key={identifier} className="glass-card overflow-hidden">
                {/* Paused row — a mouse click toggles the accordion; the
                    chevron button is the keyboard toggle (M5-close). */}
                {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions -- mouse shortcut only; the chevron button is the keyboard toggle (M5-close) */}
                <div
                  data-testid={`paused-row-${identifier}`}
                  onClick={() => {
                    togglePaused(identifier);
                  }}
                  className="flex cursor-pointer flex-wrap items-center gap-2 px-3 py-2.5"
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
                    className="text-theme-text-secondary hover:bg-theme-bg-soft inline-flex h-6 w-6 items-center justify-center rounded-full text-sm"
                  >
                    <span
                      aria-hidden="true"
                      className={`inline-block transition-transform duration-200 ${
                        isExpanded ? 'rotate-90' : ''
                      }`}
                    >
                      ›
                    </span>
                  </button>

                  <span aria-hidden="true" className="status-dot dot-warn" />

                  {/* Identifier — opens the detail slide */}
                  <button
                    type="button"
                    aria-label={`View details for paused issue ${identifier}`}
                    className="text-theme-text min-h-6 cursor-pointer font-mono text-xs font-semibold hover:underline"
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
                        className="tag pill-accent flex-shrink-0"
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
                    className="mt-1 flex w-full flex-shrink-0 flex-wrap items-center gap-1.5 sm:mt-0 sm:w-auto"
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
                      type="button"
                      onClick={() => {
                        resumeIssueMutation.mutate(identifier);
                      }}
                      className={ACTION_BUTTON}
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
      )}
    </section>
  );
}
