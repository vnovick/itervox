import { memo } from 'react';
import { fmtMs, EMPTY_PROFILE_LABEL } from '../../utils/format';
import type { TrackerIssue, ProfileDef } from '../../types/schemas';

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

// Status dot color per orchestrator state
function statusDotClass(state: string): string {
  switch (state) {
    case 'running':
      return 'bg-theme-success';
    case 'paused':
      return 'bg-theme-warning';
    case 'retrying':
      return 'bg-theme-danger';
    case 'input_required':
      return 'bg-orange-400';
    case 'pending_input_resume':
      return 'bg-orange-400';
    default:
      return 'bg-theme-muted';
  }
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

  return (
    <div
      className={`issue-card group border-theme-line bg-theme-bg-elevated cursor-pointer rounded-lg border p-3 transition-all select-none ${
        isDragging ? 'rotate-1 opacity-90 shadow-lg' : ''
      }`}
      onClick={() => {
        onSelect(issue.identifier);
      }}
    >
      {/* Row 1: Status dot + Identifier + profile/dispatch (right) */}
      <div className="flex items-center gap-2">
        {/* Status dot — Linear-style */}
        <span
          className={`border-theme-line h-3.5 w-3.5 flex-shrink-0 rounded-full border-2 ${
            hasActivity ? statusDotClass(issue.orchestratorState) : 'bg-transparent'
          }`}
        />

        {/* Identifier */}
        {issue.url ? (
          <a
            href={issue.url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-theme-text-secondary font-mono text-xs font-medium hover:underline"
            onClick={(e) => {
              e.stopPropagation();
            }}
          >
            {issue.identifier}
          </a>
        ) : (
          <span className="text-theme-text-secondary font-mono text-xs font-medium">
            {issue.identifier}
          </span>
        )}

        <div className="ml-auto flex items-center gap-1.5">
          {/* Profile selector */}
          {showProfileSelector && (
            <div
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
                className="border-theme-line bg-theme-panel-strong text-theme-text-secondary max-w-[100px] rounded border px-1.5 py-0.5 text-[10px] font-medium focus:outline-none disabled:opacity-40"
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

          {/* Read-only profile badge for non-editable cards */}
          {!isEditable && availableProfiles && availableProfiles.length > 0 && (
            <span className="border-theme-line text-theme-text-secondary flex-shrink-0 rounded border px-1.5 py-0.5 text-[10px] font-medium">
              {issue.agentProfile || EMPTY_PROFILE_LABEL}
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
              className="flex-shrink-0 rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-300"
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
              className="flex-shrink-0 rounded bg-amber-500/15 px-1.5 py-0.5 text-[10px] font-medium text-amber-300"
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
              className="flex-shrink-0 rounded bg-emerald-500/15 px-1.5 py-0.5 text-[10px] font-medium text-emerald-300 hover:bg-emerald-500/25"
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
              className="flex-shrink-0 rounded bg-sky-500/15 px-1.5 py-0.5 text-[10px] font-medium text-sky-300"
            >
              ⟳ Syncing
            </span>
          )}

          {blockerCount > 0 && (
            <span
              data-testid="issue-card-blocked-badge"
              title={`Blocked by ${String(blockerCount)} issue${blockerCount === 1 ? '' : 's'}`}
              className="bg-theme-danger-soft text-theme-danger flex-shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium"
            >
              Blocked {blockerCount}
            </span>
          )}

          {/* Dispatch button */}
          {onDispatch && (
            <button
              title="Send to queue"
              onClick={(e) => {
                e.stopPropagation();
                onDispatch(issue.identifier);
              }}
              className="text-theme-accent bg-theme-accent-soft flex-shrink-0 rounded px-1.5 py-0.5 text-[10px] opacity-0 transition-opacity group-hover:opacity-100"
            >
              ▶
            </button>
          )}
        </div>
      </div>

      {/* Row 2: Title — Linear-style: single line, truncated */}
      <p className="text-theme-text mt-1.5 truncate text-sm font-medium">{issue.title}</p>

      {/* Row 3: Bottom metadata — compact badges */}
      <div className="mt-2 flex items-center gap-1.5">
        {/* Backend badge */}
        <span
          className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${
            backend === 'codex'
              ? 'bg-theme-teal-soft text-theme-teal'
              : 'bg-theme-accent-soft text-theme-accent-strong'
          }`}
        >
          {backend === 'codex' ? 'Codex' : 'Claude'}
        </span>

        {/* State badge — only when active */}
        {isInputRequired && (
          <span className="rounded bg-orange-500/15 px-1.5 py-0.5 text-[10px] font-medium text-orange-400">
            Needs Input
          </span>
        )}
        {isPendingInputResume && (
          <span className="rounded bg-orange-500/15 px-1.5 py-0.5 text-[10px] font-medium text-orange-400">
            Resuming
          </span>
        )}
        {hasActivity && !isInputRequired && !isPendingInputResume && (
          <span
            className={`rounded px-1.5 py-0.5 text-[10px] font-medium capitalize ${
              isRunning
                ? 'bg-theme-success-soft text-theme-success'
                : 'bg-theme-warning-soft text-theme-warning'
            }`}
          >
            {issue.orchestratorState}
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
