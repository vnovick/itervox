import { useNavigate } from 'react-router';
import Badge from '../ui/badge/Badge';
import { stateBadgeColor, formatOrchestratorState } from '../../utils/format';
import type {
  AutomationDef,
  HistoryRow,
  ProfileDef,
  RunningRow,
  TrackerIssue,
} from '../../types/schemas';

/**
 * The badges + title block at the top of the issue-detail slide. Extracted
 * from `IssueDetailSlide.tsx` (T-20) so the parent can stay focused on
 * actions (mutations, profile changes, comment composition) and so the
 * read-only header can be tested in isolation if needed.
 *
 * Resolves the displayed backend in priority order:
 *  1. The currently running worker's backend (most accurate while live).
 *  2. The configured profile's backend or command (covers between-runs).
 *  3. The default backend.
 */
interface IssueDetailHeaderProps {
  issue: TrackerIssue;
  runningRows: RunningRow[] | undefined;
  history?: HistoryRow[];
  profileDefs: Record<string, ProfileDef> | undefined;
  defaultBackend: string;
  /** T-9: configured automations are used to detect stale automation IDs. */
  automations?: AutomationDef[];
  /** outbox #54 fast-follow: true when issue.identifier is a member of
   * snapshot.outboxSyncing — same join-by-identifier BoardView's
   * DraggableCard has used since Task 4 (accepted-minor D on the final
   * review: ListView/IssueDetailSlide had no equivalent marker). */
  syncing?: boolean;
}

/**
 * The header: badges, then the title on its own line. With `badges={false}`
 * only the title renders — the slide draws the badges inline after the
 * identifier (IssueDetailBadges in the panel's title row).
 */
export default function IssueDetailHeader({
  badges = true,
  ...props
}: IssueDetailHeaderProps & { badges?: boolean }) {
  return (
    <div className="flex-shrink-0 space-y-3 px-6 pb-3">
      {badges && <IssueDetailBadges {...props} />}
      <p className="text-theme-text text-xl leading-tight font-semibold tracking-[-0.01em]">
        {props.issue.title}
      </p>
    </div>
  );
}

/** State, run state, sync / automation / sandbox markers and the backend tag. */
export function IssueDetailBadges({
  issue,
  runningRows,
  history,
  profileDefs,
  defaultBackend,
  automations,
  syncing,
}: IssueDetailHeaderProps) {
  const runningRow = runningRows?.find((r) => r.identifier === issue.identifier);
  const runningBackend = runningRow?.backend;
  const profileHint =
    issue.agentProfile && profileDefs?.[issue.agentProfile]
      ? profileDefs[issue.agentProfile].backend || profileDefs[issue.agentProfile].command || ''
      : '';
  const backend =
    runningBackend ||
    (profileHint &&
      (/codex/i.test(profileHint) ? 'codex' : /claude/i.test(profileHint) ? 'claude' : '')) ||
    defaultBackend;

  // T-9: latest run's automation context. Live runs win; otherwise fall back
  // to the newest matching history row.
  const latestAutomationId = (() => {
    if (runningRow?.automationId) return runningRow.automationId;
    if (!history) return '';
    let best = '';
    let bestT = -Infinity;
    for (const h of history) {
      if (h.identifier !== issue.identifier || !h.automationId) continue;
      const t = h.finishedAt ? new Date(h.finishedAt).getTime() : 0;
      if (t > bestT) {
        bestT = t;
        best = h.automationId;
      }
    }
    return best;
  })();
  const automationStale =
    latestAutomationId !== '' &&
    !!automations &&
    !automations.some((a) => a.id === latestAutomationId);

  const navigate = useNavigate();
  const onAutomationClick = () => {
    if (!latestAutomationId || automationStale) return;
    void navigate(`/automations?openAutomation=${encodeURIComponent(latestAutomationId)}`);
  };

  return (
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
      <Badge color={stateBadgeColor(issue.state)} size="sm">
        {issue.state}
      </Badge>
      <Badge
        color={
          issue.orchestratorState === 'running'
            ? 'success'
            : issue.orchestratorState === 'retrying' ||
                issue.orchestratorState === 'pending_input_resume'
              ? 'warning'
              : 'light'
        }
        size="sm"
      >
        {formatOrchestratorState(issue.orchestratorState)}
      </Badge>
      {syncing && (
        <span
          data-testid="issue-detail-syncing-badge"
          title="A tracker state update for this issue is queued and not yet confirmed by the tracker"
          className="pill pill-accent flex-shrink-0 px-2 py-0.5 text-[10px] leading-4"
        >
          ⟳ Syncing
        </span>
      )}
      {latestAutomationId && (
        <button
          type="button"
          data-testid="automation-source-badge"
          onClick={onAutomationClick}
          disabled={automationStale}
          title={
            automationStale
              ? 'Rule deleted'
              : `Triggered by automation ${latestAutomationId} — click to open the rule`
          }
          className={
            'pill px-2 py-0.5 text-[10px] leading-4 transition-colors ' +
            (automationStale
              ? 'pill-neutral cursor-not-allowed line-through opacity-60'
              : 'pill-ok hover:brightness-95')
          }
        >
          🤖 {latestAutomationId}
        </button>
      )}
      {runningRow?.isolation && (
        <span
          data-testid="issue-detail-isolation-badge"
          title="The agent runs in an operating-system sandbox: it can write only to its workspace, and credentials and other workspaces are hidden"
          className="pill pill-ok flex-shrink-0 px-2 py-0.5 text-[10px] leading-4"
        >
          Sandboxed ({runningRow.isolation})
        </span>
      )}
      <span className="tag ml-auto">{backend}</span>
    </div>
  );
}
