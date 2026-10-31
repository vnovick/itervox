import { useRunContext } from '../../../queries/changes';
import { DiffFileList } from './DiffFileList';
import { fmtTokens } from '../../../utils/format';

const NOW_FILES_SHOWN = 8;

/**
 * #215 — the issue detail's "Now" panel: what the issue's run is doing
 * (its last step and tool call, never tool output), its counters, what is
 * waiting on a human, the workspace's port (#250) and the files changed so
 * far. Polls while the run is live; shows the last finished run otherwise.
 * Renders nothing when the issue has neither a run nor a worktree.
 */
export function IssueNowPanel({ identifier, live }: { identifier: string; live: boolean }) {
  const { data } = useRunContext(identifier, { enabled: true, live });
  if (!data) return null;
  const run = data.run;
  if (!run && !data.port && data.changedFiles.length === 0 && data.handoffs.length === 0)
    return null;
  return (
    <section
      className="border-theme-line bg-theme-bg-soft space-y-2 rounded-[var(--radius-sm)] border p-3"
      data-testid="issue-now-panel"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-theme-text text-xs font-medium tracking-wider uppercase">
          {run?.live ? 'Now' : 'Last run'}
        </h4>
        {run && (
          <span className="text-theme-muted text-xs">
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
      {run?.currentStep && <p className="text-theme-text text-sm">{run.currentStep}</p>}
      {run?.lastToolCall && (
        <p className="text-xs" data-testid="now-last-tool">
          <span className="text-theme-muted">Last tool: </span>
          <span className="text-theme-text font-medium">{run.lastToolCall.name}</span>
          {run.lastToolCall.target && (
            <code className="text-theme-text-secondary ml-1 font-mono break-all">
              {run.lastToolCall.target}
            </code>
          )}
          <span className="text-theme-muted"> · {run.toolCalls} calls</span>
        </p>
      )}
      {run?.pendingInput && (
        <p className="text-theme-warning-text text-xs">
          {run.pendingApproval ? 'Waiting for approval: ' : 'Waiting for input: '}
          {run.pendingInput}
        </p>
      )}
      {run && (run.queuedSteers > 0 || run.steers.length > 0) && (
        <p className="text-theme-muted text-xs">
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
