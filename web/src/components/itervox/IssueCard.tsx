import { memo, useState } from 'react';
import type { DraggableAttributes } from '@dnd-kit/core';
import { GripVertical } from 'lucide-react';
import { fmtMs, EMPTY_PROFILE_LABEL, orchStatusDotClass } from '../../utils/format';
import type { TrackerIssue, ProfileDef } from '../../types/schemas';
import { WhyIdleChip } from './WhyIdleChip';

/**
 * CORE-068: the dnd-kit keyboard activator lives on a dedicated drag handle
 * (registered via `setActivatorNodeRef`), so Space/Enter only start a
 * keyboard drag when the handle itself is focused. The rest of the card —
 * the title button, the review badge — keep their native key behaviour.
 */
interface CardProps {
  issue: TrackerIssue;
  isDragging?: boolean;
  onSelect: (id: string) => void;
  availableProfiles?: string[];
  profileDefs?: Record<string, ProfileDef>;
  runningBackend?: string;
  defaultBackend?: string;
  onProfileChange?: (identifier: string, profile: string) => void;
  onDispatch?: (identifier: string) => void;
  // T-6: optional surface fields populated from the latest matching
  // RunningRow / HistoryRow. When the latest run was a reviewer with
  // commentCount > 0, the card renders a small "📝 N reviews" badge.
  runningKind?: string;
  commentCount?: number;
  // Gap A: when the issue is input_required and its corresponding snapshot
  // entry has been queued past the configured stale threshold, the card
  // renders a small "Stale" badge with the age in its tooltip so the
  // operator can spot abandoned issues at a glance.
  inputRequiredStale?: boolean;
  inputRequiredAgeMinutes?: number;
  // G: when the issue is currently mid-retry, surface "↻ retry N/M" so the
  // operator can spot which issues are burning through their budget. M is
  // the global cfg.Agent.MaxRetries (0 → ∞).
  retryAttempt?: number;
  maxRetries?: number;
  // outbox Task 4: true when this issue's identifier is a member of
  // snapshot.outboxSyncing — a write-ahead-outbox update_state entry is
  // pending for it (queued durably, not yet flushed to the tracker). The
  // card badge is the operator-visible half of the overlay: the dashboard
  // is showing the about-to-be-true state, not necessarily what the
  // tracker itself currently reports.
  syncing?: boolean;
  /** Board-only: dnd-kit attributes for the drag handle. Omitted in overlays and lists. */
  dragHandleAttributes?: DraggableAttributes;
  /** Board-only: dnd-kit `setActivatorNodeRef` for the drag handle. */
  dragHandleRef?: (element: HTMLElement | null) => void;
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
    // Explicit backend field takes priority, then infer from command name.
    const hint = def.backend || def.command || '';
    if (hint) return /codex/i.test(hint) ? 'codex' : 'claude';
  }
  const fallback = defaultBackend || '';
  return /codex/i.test(fallback) ? 'codex' : 'claude';
}

export default memo(function IssueCard({
  issue,
  isDragging,
  onSelect,
  availableProfiles,
  profileDefs,
  runningBackend,
  defaultBackend,
  onProfileChange,
  onDispatch,
  runningKind,
  commentCount,
  inputRequiredStale,
  inputRequiredAgeMinutes,
  retryAttempt,
  maxRetries,
  syncing,
  dragHandleAttributes,
  dragHandleRef,
}: CardProps) {
  const isRunning = issue.orchestratorState === 'running';
  const isInputRequired = issue.orchestratorState === 'input_required';
  const isPendingInputResume = issue.orchestratorState === 'pending_input_resume';
  const isActive =
    isRunning ||
    isInputRequired ||
    isPendingInputResume ||
    issue.orchestratorState === 'paused' ||
    issue.orchestratorState === 'retrying';
  // Show dropdown only in backlog columns (signaled by onDispatch being present)
  // or when the issue is idle and not in a terminal/completion state.
  const isEditable = !!onDispatch && !isActive;
  const showProfileSelector =
    isEditable && availableProfiles && availableProfiles.length > 0 && onProfileChange;
  const backend = resolveBackend(issue.agentProfile, profileDefs, runningBackend, defaultBackend);
  const hasActivity = isActive;
  const blockerCount = getBlockerCount(issue);
  // CORE-024 — the dispatch button is otherwise only revealed by
  // `group-hover:opacity-100`, which a keyboard user tabbing to it without a
  // mouse never triggers. Tailwind's `focus-visible:opacity-100` handles
  // this in production CSS; the local `dispatchFocused` state is a small
  // belt-and-suspenders duplicate that also makes the behavior verifiable
  // without a built stylesheet (Vitest's jsdom does not process CSS).
  const [dispatchFocused, setDispatchFocused] = useState(false);

  return (
    // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions -- mouse shortcut only; the title button is the keyboard/AT way to open the issue (CORE-068)
    <div
      className={`issue-card group glass-card focus-within:border-theme-accent flex cursor-pointer flex-col gap-1.5 px-3 py-2.5 transition-all select-none ${
        isDragging ? 'rotate-1 opacity-90 shadow-lg' : ''
      }`}
      onClick={() => {
        onSelect(issue.identifier);
      }}
    >
      {/* Row 1: drag handle + status dot + identifier, profile/dispatch (right) */}
      <div className="flex min-h-5 items-center gap-1.5">
        {dragHandleAttributes && (
          <button
            type="button"
            ref={dragHandleRef}
            {...dragHandleAttributes}
            aria-label={`Move ${issue.identifier}`}
            data-testid="issue-card-drag-handle"
            data-drag-handle-for={issue.identifier}
            onClick={(e) => {
              // A click on the handle is not a request to open the issue.
              e.stopPropagation();
            }}
            className="text-theme-muted hover:text-theme-text-secondary -ml-1 flex h-5 w-4 flex-shrink-0 cursor-grab items-center justify-center rounded"
          >
            <GripVertical size={14} aria-hidden="true" />
          </button>
        )}
        {/* Status dot — only while the orchestrator holds the issue */}
        {hasActivity && (
          <span
            aria-hidden="true"
            className={`status-dot ${orchStatusDotClass(issue.orchestratorState)}`}
          />
        )}

        {/* Identifier */}
        {issue.url ? (
          <a
            href={issue.url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-theme-muted font-mono text-[11px] hover:underline"
            onClick={(e) => {
              e.stopPropagation();
            }}
          >
            {issue.identifier}
          </a>
        ) : (
          <span className="text-theme-muted font-mono text-[11px]">{issue.identifier}</span>
        )}

        <div className="ml-auto flex items-center gap-1.5">
          {/* Profile selector */}
          {showProfileSelector && (
            <div
              role="presentation"
              className="flex-shrink-0"
              onClick={(e) => {
                e.stopPropagation();
              }}
            >
              <select
                value={issue.agentProfile ?? ''}
                onChange={(e) => {
                  onProfileChange(issue.identifier, e.target.value);
                }}
                disabled={isRunning}
                aria-label={`Agent profile for ${issue.identifier}`}
                className="subtle-fill text-theme-text-secondary focus-visible:ring-theme-accent max-w-[110px] rounded-full border-0 px-2 py-0.5 text-[11px] font-medium focus:outline-none focus-visible:ring-2 disabled:opacity-40"
              >
                <option value="">{EMPTY_PROFILE_LABEL}</option>
                {availableProfiles.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </div>
          )}

          {/* Dispatch button — visible on hover OR keyboard focus (CORE-024) */}
          {onDispatch && (
            <button
              title="Send to queue"
              onClick={(e) => {
                e.stopPropagation();
                onDispatch(issue.identifier);
              }}
              onFocus={() => {
                setDispatchFocused(true);
              }}
              onBlur={() => {
                setDispatchFocused(false);
              }}
              style={dispatchFocused ? { opacity: 1 } : undefined}
              className="tag pill-accent flex-shrink-0 opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
            >
              ▶
            </button>
          )}
        </div>
      </div>

      {/* Row 2: Title — wraps to two lines */}
      {/* CORE-068: a real button, so the card opens from the keyboard (Enter
          or Space) without going through the drag handle. */}
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          onSelect(issue.identifier);
        }}
        className="text-theme-text line-clamp-2 block w-full cursor-pointer rounded-sm text-left text-[13px] leading-[1.35] font-medium focus-visible:underline"
      >
        {issue.title}
      </button>

      {/* Row 3: tags — profile, backend, run state and every condition badge */}
      <div className="flex flex-wrap items-center gap-[5px]">
        {/* Read-only profile tag for non-editable cards */}
        {!isEditable && availableProfiles && availableProfiles.length > 0 && (
          <span className="tag">{issue.agentProfile || EMPTY_PROFILE_LABEL}</span>
        )}

        {/* Backend tag */}
        <span className="tag">{backend === 'codex' ? 'Codex' : 'Claude'}</span>

        {/* State tag — only when active */}
        {isInputRequired && <span className="tag pill-warn">Needs Input</span>}
        {isPendingInputResume && <span className="tag pill-warn">Resuming</span>}
        {hasActivity && !isInputRequired && !isPendingInputResume && (
          <span className={`tag capitalize ${isRunning ? 'pill-ok' : 'pill-warn'}`}>
            {issue.orchestratorState}
          </span>
        )}

        {/* Gap A: stale-input-required badge. Renders only when the
            issue is currently input_required AND the snapshot row's
            `stale` flag is set (orchestrator picks a threshold based on
            the longest configured max_age_minutes across input_required
            automations). Tooltip shows the wall-clock age. */}
        {isInputRequired && inputRequiredStale && (
          <span
            data-testid="issue-card-stale-badge"
            title={
              inputRequiredAgeMinutes
                ? `Input requested ${formatAge(inputRequiredAgeMinutes)} ago — likely abandoned`
                : 'Input request is stale — likely abandoned'
            }
            className="tag pill-warn"
          >
            ⚠ Stale
          </span>
        )}

        {/* G: retry-in-flight badge. Visible only while the orchestrator
            is actually backing off this issue. The "M" denominator falls
            back to ∞ when max_retries=0 (unlimited). */}
        {issue.orchestratorState === 'retrying' && retryAttempt !== undefined && (
          <span
            data-testid="issue-card-retry-badge"
            title={`Retry attempt ${String(retryAttempt)}${
              maxRetries && maxRetries > 0 ? ` of ${String(maxRetries)}` : ' (unlimited)'
            }`}
            className="tag pill-warn"
          >
            {`↻ retry ${String(retryAttempt)}${
              maxRetries && maxRetries > 0 ? `/${String(maxRetries)}` : ''
            }`}
          </span>
        )}

        {/* T-6: review-comment badge. Renders when the latest run was a
            reviewer that posted at least one comment. Click bubbles up
            to onSelect — the issue detail slide opens with the run
            section in scroll. */}
        {runningKind === 'reviewer' && (commentCount ?? 0) > 0 && (
          <button
            type="button"
            data-testid="issue-card-review-badge"
            title={`Reviewer posted ${String(commentCount)} comment${(commentCount ?? 0) === 1 ? '' : 's'} on this issue`}
            onClick={(e) => {
              e.stopPropagation();
              onSelect(issue.identifier);
            }}
            className="tag pill-warn"
          >
            📝 {commentCount} {commentCount === 1 ? 'review' : 'reviews'}
          </button>
        )}

        {/* outbox Task 4: a pending write-ahead-outbox transition for
            this issue. Distinct from the retry/stale badges above — this
            isn't an agent-side condition, it's "the daemon has a durable
            write queued for this issue that hasn't reached the tracker
            yet". */}
        {syncing && (
          <span
            data-testid="issue-card-syncing-badge"
            title="A tracker state update for this issue is queued and not yet confirmed by the tracker"
            className="tag pill-accent"
          >
            ⟳ Syncing
          </span>
        )}

        {/* CORE-055: the next run uses an automatic override (a
            rate_limited automation or backend_fallback), not a pin. */}
        {issue.autoSwitch && (
          <span
            data-testid="issue-card-auto-switch-badge"
            title={`Auto-switched from ${issue.autoSwitch.fromBackend ?? 'unknown'} by ${issue.autoSwitch.source}${
              issue.autoSwitch.reason ? `: ${issue.autoSwitch.reason}` : ''
            }`}
            className="tag pill-warn"
          >
            {issue.autoSwitch.toBackend ?? issue.agentBackend ?? 'switched'} (auto)
          </span>
        )}

        {/* #218 — the task-type playbook the issue's run gets. */}
        {issue.playbook && (
          <span
            data-testid="issue-card-playbook-badge"
            title={`Playbook: ${issue.playbook}${issue.playbookSource ? ` (${issue.playbookSource})` : ''}`}
            className="tag"
          >
            {issue.playbook}
          </span>
        )}

        {/* CORE-080 — why this idle issue is not dispatching. */}
        <WhyIdleChip reason={issue.ineligibleReason} />

        {blockerCount > 0 && (
          <span
            data-testid="issue-card-blocked-badge"
            title={`Blocked by ${String(blockerCount)} issue${blockerCount === 1 ? '' : 's'}`}
            className="tag pill-warn"
          >
            Blocked {blockerCount}
          </span>
        )}

        {/* Elapsed */}
        {(issue.elapsedMs ?? 0) > 0 && (
          <span className="text-theme-muted ml-auto font-mono text-[10px]">
            {fmtMs(issue.elapsedMs ?? 0)}
          </span>
        )}
      </div>
    </div>
  );
});

// formatAge is local to the stale-badge tooltip — turns a minute count
// into something human-readable like "2h" or "3d 4h" without dragging in
// a date-fns dependency. Returns "" for non-positive values so the caller
// can fall back to a generic message.
function formatAge(minutes: number): string {
  if (!Number.isFinite(minutes) || minutes <= 0) return '';
  if (minutes < 60) return `${String(minutes)}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    const remM = minutes % 60;
    return remM ? `${String(hours)}h ${String(remM)}m` : `${String(hours)}h`;
  }
  const days = Math.floor(hours / 24);
  const remH = hours % 24;
  return remH ? `${String(days)}d ${String(remH)}h` : `${String(days)}d`;
}

function getBlockerCount(issue: TrackerIssue): number {
  const detailCount = issue.blockedByDetails?.length ?? 0;
  if (detailCount > 0) return detailCount;
  return issue.blockedBy?.length ?? 0;
}
