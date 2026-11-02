import { useRunContext } from '../../../queries/changes';
import { DiffFileList } from './DiffFileList';
import { fmtTokens } from '../../../utils/format';
import { useItervoxStore } from '../../../store/itervoxStore';
import { SYSTEM_VISIBILITY_CAPABILITY } from '../../../queries/activity';
import { RunResources } from '../resources/RunResources';
import { IssueActivity } from '../resources/IssueActivity';

const NOW_FILES_SHOWN = 8;

/**
 * #215 — the issue detail's "Now" panel: what the issue's run is doing
 * (its last step and tool call, never tool output), its counters, what is
 * waiting on a human, the workspace's port (#250) and the files changed so
 * far, its resource use and activity audit (system visibility). Polls while
 * the run is live; shows the last finished run otherwise.
 * Renders nothing when the issue has neither a run nor a worktree.
 */
export function IssueNowPanel({ identifier, live }: { identifier: string; live: boolean }) {
  const { data } = useRunContext(identifier, { enabled: true, live });
  const systemVisibility = useItervoxStore(
    (s) => s.snapshot?.capabilities?.includes(SYSTEM_VISIBILITY_CAPABILITY) ?? false,
  );
  if (!data) return null;
  const run = data.run;
  if (!run && !data.port && data.changedFiles.length === 0 && data.handoffs.length === 0)
    return null;
  return (
    <section
      className="glass-card space-y-2 rounded-[22px] px-4 py-3.5"
      data-testid="issue-now-panel"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-theme-text flex items-center gap-2 text-sm font-semibold">
          {run?.live && (
            <span
              aria-hidden="true"
              className="status-dot dot-ok ring-theme-success-soft ring-[3px]"
            />
          )}
          {run?.live ? 'Now' : 'Last run'}
        </h4>
        {run && (
          <span className="text-theme-text-secondary text-xs">
            {[
              run.profile,
              run.backend,
              `turn ${String(run.turnCount)}`,
              `${fmtTokens(run.totalTokens)} tok`,
            ]
              .filter(Boolean)
              .join(' · ')}
            {!run.live && run.outcome ? ` · ${run.outcome}` : ''}
          </span>
        )}
      </div>
      {data.port ? (
        <p className="text-xs" data-testid="now-port">
          <span className="text-theme-muted">Port: </span>
          <code className="text-theme-text font-mono">{data.port}</code>
          <span className="text-theme-muted"> (ITERVOX_PORT)</span>
        </p>
      ) : null}
      {run?.currentStep && (
        <p className="text-theme-text text-sm leading-[1.45]">{run.currentStep}</p>
      )}
      {run?.lastToolCall && (
        <p className="code-line" data-testid="now-last-tool">
          <span className="sr-only">Last tool: </span>
          <span className="font-semibold">{run.lastToolCall.name}</span>
          {run.lastToolCall.target && (
            <code className="ml-1 break-all">· {run.lastToolCall.target}</code>
          )}
          <span className="opacity-75"> · {run.toolCalls} calls</span>
        </p>
      )}
      {run?.resources && <RunResources usage={run.resources} />}
      {run?.pendingInput && (
        <p className="text-theme-warning-text text-xs">
          {run.pendingApproval ? 'Waiting for approval: ' : 'Waiting for input: '}
          {run.pendingInput}
        </p>
      )}
      {run && (run.queuedSteers > 0 || run.steers.length > 0) && (
        <p className="text-theme-text-secondary text-xs">
          Steering: {run.steers.length} delivered
          {run.queuedSteers > 0 ? `, ${String(run.queuedSteers)} queued` : ''}
        </p>
      )}
      {run && run.guardrailBlocks > 0 && (
        <p className="text-theme-danger-text text-xs">
          Guardrails blocked {run.guardrailBlocks}{' '}
          {run.guardrailBlocks === 1 ? 'operation' : 'operations'}
          {run.lastGuardrailKind ? ` (last: ${run.lastGuardrailKind})` : ''}
        </p>
      )}
      {data.changedFiles.length > 0 && (
        <div>
          <p className="text-theme-muted mb-1 text-xs">
            Changed files ({data.changedFiles.length})
          </p>
          <DiffFileList files={data.changedFiles} limit={NOW_FILES_SHOWN} />
        </div>
      )}
      {systemVisibility && <IssueActivity identifier={identifier} live={live} />}
      {data.handoffs.length > 0 && (
        <div>
          <p className="text-theme-muted mb-1 text-xs">Prior handoffs</p>
          <ul className="space-y-0.5 text-xs">
            {data.handoffs.map((h) => (
              <li key={h.path} className="text-theme-text truncate" title={h.path}>
                {h.title}
                {h.partial ? <span className="text-theme-warning-text"> (partial)</span> : null}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
