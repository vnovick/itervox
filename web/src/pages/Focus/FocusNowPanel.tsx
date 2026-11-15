import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../store/itervoxStore';
import { useRunContext } from '../../queries/changes';
import { useDismissInput, useProvideInput } from '../../queries/issues';
import { IssueDetailBadges } from '../../components/itervox/IssueDetailHeader';
import { IssueSteerBox } from '../../components/itervox/IssueSteerBox';
import { InputRequiredPanel } from '../../components/itervox/InputRequiredPanel';
import { plainButtonClass } from '../../components/ui/button/buttonRecipe';
import { useNow } from '../../hooks/useNow';
import { fmtAge } from '../../utils/format';
import type { TrackerIssue } from '../../types/schemas';
import { SYSTEM_VISIBILITY_CAPABILITY } from '../../queries/activity';
import { RunResources } from '../../components/itervox/resources/RunResources';
import { IssueActivity } from '../../components/itervox/resources/IssueActivity';

const LINK_BUTTON = plainButtonClass('secondary', 'sm', { pill: true });

/**
 * The issue view's left column: what the issue's agent is doing now (its
 * latest step and last tool call, never tool output), its counters, the
 * question it is waiting on, and the steer box while the run is live.
 */
export function FocusNowPanel({
  issue,
  runContext,
}: {
  issue: TrackerIssue;
  /** The daemon publishes run context (#215). */
  runContext: boolean;
}) {
  const live = issue.orchestratorState === 'running';
  const { data } = useRunContext(issue.identifier, { enabled: runContext, live });
  const run = data?.run ?? null;
  const now = useNow(live ? 1_000 : 60_000);
  const snap = useItervoxStore(
    useShallow((s) => ({
      runningRows: s.snapshot?.running,
      history: s.snapshot?.history,
      profileDefs: s.snapshot?.profileDefs,
      defaultBackend: s.snapshot?.defaultBackend ?? 'claude',
      automations: s.snapshot?.automations,
      inlineInput: s.snapshot?.inlineInput ?? false,
      systemVisibility: s.snapshot?.capabilities?.includes(SYSTEM_VISIBILITY_CAPABILITY) ?? false,
    })),
  );
  const setSelected = useItervoxStore((s) => s.setSelectedIdentifier);
  const provideInput = useProvideInput();
  const dismissInput = useDismissInput();

  const elapsed = run?.live ? fmtAge(run.startedAt, now) : null;
  const meta = [run?.profile, run?.backend, elapsed].filter(Boolean).join(' · ');
  const heading = run?.live ? 'Now' : run ? 'Last run' : 'No run yet';
  const chips = run
    ? [
        `${String(run.toolCalls)} tool ${run.toolCalls === 1 ? 'call' : 'calls'}`,
        `${String(run.turnCount)} ${run.turnCount === 1 ? 'turn' : 'turns'}`,
        run.playbook ? `Playbook: ${run.playbook}` : '',
        !run.live && run.outcome ? `Outcome: ${run.outcome}` : '',
      ].filter(Boolean)
    : [];

  return (
    <section
      aria-labelledby="focus-now-heading"
      data-testid="focus-now"
      className="glass-panel flex min-w-0 flex-col gap-3 px-5 py-[18px]"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span
          aria-hidden="true"
          className={`status-dot ${run?.live ? 'dot-ok ring-theme-success-soft ring-[3px]' : 'dot-idle'}`}
        />
        <h2 id="focus-now-heading" className="section-title">
          {heading}
        </h2>
        {meta && <span className="text-theme-text-secondary ml-auto text-xs">{meta}</span>}
      </div>
      {/* Wrapped: the badges row grows (flex-1) in its usual row layout. */}
      <div className="flex">
        <IssueDetailBadges
          issue={issue}
          runningRows={snap.runningRows}
          history={snap.history}
          profileDefs={snap.profileDefs}
          defaultBackend={snap.defaultBackend}
          automations={snap.automations}
        />
      </div>
      <p
        data-testid="focus-title"
        className="text-theme-text text-lg leading-[1.3] font-semibold tracking-[-0.01em]"
      >
        {issue.title}
      </p>
      {run?.currentStep && (
        <p className="glass-chip text-theme-text rounded-[14px] px-3.5 py-3 text-sm leading-[1.45]">
          {run.currentStep}
        </p>
      )}
      {run?.lastToolCall && (
        <div className="space-y-1.5">
          <p className="text-theme-text-secondary text-[13px]">Last tool call</p>
          <p className="code-line" data-testid="focus-last-tool">
            {run.lastToolCall.name}
            {run.lastToolCall.target ? ` · ${run.lastToolCall.target}` : ''}
          </p>
        </div>
      )}
      {chips.length > 0 && (
        <ul aria-label="Run counters" className="flex flex-wrap gap-2 text-xs">
          {chips.map((c) => (
            <li key={c} className="glass-chip text-theme-text px-2.5 py-1">
              {c}
            </li>
          ))}
        </ul>
      )}
      {run?.resources && <RunResources usage={run.resources} />}
      {snap.systemVisibility && <IssueActivity identifier={issue.identifier} live={live} />}
      {run?.pendingInput && !live && issue.orchestratorState !== 'input_required' && (
        <p className="text-theme-warning-text text-xs">Waiting for input: {run.pendingInput}</p>
      )}
      <InputRequiredPanel
        issue={issue}
        inlineInput={snap.inlineInput}
        provideInputMutation={provideInput}
        dismissInputMutation={dismissInput}
      />
      {!runContext && (
        <p className="text-theme-muted text-xs italic">
          This daemon does not publish run details; open the issue for its history.
        </p>
      )}
      <div aria-hidden="true" className="flex-1" />
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          className={LINK_BUTTON}
          onClick={() => {
            setSelected(issue.identifier);
          }}
        >
          Issue details
        </button>
        {issue.url && (
          <a
            href={issue.url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-theme-accent-text text-sm hover:underline"
          >
            View in tracker →
          </a>
        )}
      </div>
      {runContext && live && <IssueSteerBox identifier={issue.identifier} />}
    </section>
  );
}
