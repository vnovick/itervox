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
export default function IssueDetailHeader({
  issue,
  runningRows,
  history,
  profileDefs,
  defaultBackend,
  automations,
}: {
  issue: TrackerIssue;
  runningRows: RunningRow[] | undefined;
  history?: HistoryRow[];
  profileDefs: Record<string, ProfileDef> | undefined;
  defaultBackend: string;
  /** T-9: configured automations are used to detect stale automation IDs. */
  automations?: AutomationDef[];
}) {
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
    <div className="border-theme-line flex-shrink-0 space-y-1 border-b px-5 py-3">
      <div className="flex flex-wrap items-center gap-2">
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
              'rounded-full px-2 py-0.5 text-[10px] font-medium transition-colors ' +
              (automationStale
                ? 'bg-theme-bg-soft text-theme-muted/60 cursor-not-allowed line-through'
                : 'bg-emerald-500/15 text-emerald-300 hover:bg-emerald-500/25')
            }
          >
            🤖 {latestAutomationId}
          </button>
        )}
        <span className="bg-theme-bg-soft text-theme-text-secondary ml-auto rounded-full px-2 py-0.5 text-[10px] font-medium">
          {backend}
        </span>
      </div>
      <p className="text-theme-text text-xl leading-tight font-semibold">{issue.title}</p>
    </div>
  );
}
