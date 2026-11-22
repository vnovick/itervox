import type { EvidenceGate, EvidenceRun } from '../../../types/schemas';
import { fmtMs } from '../../../utils/format';
import { evidenceTime, shortDigest } from './evidenceFormat';

function statusPill(status: string): string {
  if (status === 'succeeded') return 'pill-ok';
  if (status === 'running') return 'pill-neutral';
  if (status === 'input_required') return 'pill-warn';
  return 'pill-danger';
}

function GateLine({ name, gate }: { name: string; gate?: EvidenceGate }) {
  if (!gate) return null;
  return (
    <li data-testid="evidence-gate">
      <span className="text-theme-muted">{name}</span>{' '}
      <span className="font-mono">{gate.required.join(', ') || '—'}</span>{' '}
      <span className={`pill pill-sm ${gate.passed ? 'pill-ok' : 'pill-danger'}`}>
        {gate.passed ? 'passed' : 'failed'}
      </span>
      {gate.failed.length > 0 && (
        <span className="text-theme-danger-text"> missing {gate.failed.join(', ')}</span>
      )}
    </li>
  );
}

/** One fact of a run, hidden when empty. */
function Fact({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  if (!value) return null;
  return (
    <span>
      <span className="text-theme-muted">{label}</span>{' '}
      <span className={mono ? 'font-mono' : undefined}>{value}</span>
    </span>
  );
}

function RunItem({ run }: { run: EvidenceRun }) {
  const agent = [run.backend, run.profileModel, run.workerHost].filter(Boolean).join(' · ');
  const checks = run.checks;
  return (
    <li data-testid="evidence-run" className="text-xs">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-theme-text font-medium">{run.profile ?? 'default'}</span>
        <span className="text-theme-muted">{run.kind}</span>
        <span className={`pill pill-sm ${statusPill(run.status)}`}>{run.status}</span>
        <span className="text-theme-muted">{evidenceTime(run.startedAt)}</span>
        <span className="text-theme-muted">{fmtMs(run.elapsedMs)}</span>
        {run.retryAttempt ? (
          <span className="pill pill-neutral pill-sm">retry {run.retryAttempt}</span>
        ) : null}
      </div>
      <div className="text-theme-text mt-1 flex flex-wrap gap-x-3 gap-y-0.5">
        <Fact label="run" value={run.runId} mono />
        <Fact label="agent" value={agent} />
        <Fact label="prompt" value={shortDigest(run.promptDigest)} mono />
        <Fact
          label="rules"
          value={run.ruleIds.length > 0 ? run.ruleIds.join(', ') : shortDigest(run.rulesHash)}
        />
        <Fact label="memory" value={run.memoryIds.join(', ')} />
        <Fact label="playbook" value={run.playbook} />
        <Fact label="verdict" value={run.reviewVerdict} />
      </div>
      {checks && (checks.evidence ?? checks.signed) && (
        <ul className="mt-1 space-y-0.5">
          <GateLine name="Evidence gate" gate={checks.evidence} />
          <GateLine name="Signed checks" gate={checks.signed} />
        </ul>
      )}
      {run.steers.length > 0 && (
        <details className="mt-1">
          <summary className="text-theme-muted cursor-pointer">
            {run.steers.length} steering message{run.steers.length === 1 ? '' : 's'}
            {run.steersDropped ? ` (${String(run.steersDropped)} older dropped)` : ''}
          </summary>
          <ul className="mt-0.5 space-y-0.5 pl-3">
            {run.steers.map((s) => (
              <li key={`${s.at}-${s.delivery}`}>
                <span className="text-theme-muted">
                  {evidenceTime(s.at)} · {s.delivery}
                </span>
                {s.text ? `: ${s.text}` : ''}
              </li>
            ))}
          </ul>
        </details>
      )}
    </li>
  );
}

/** What ran: the issue's runs, oldest first, the live run last. */
export function EvidenceRuns({ runs }: { runs: readonly EvidenceRun[] }) {
  if (runs.length === 0) {
    return <p className="text-theme-muted text-xs">No runs recorded for this issue.</p>;
  }
  return (
    <ol className="border-theme-line space-y-3 border-l pl-3">
      {runs.map((run) => (
        <RunItem key={`${run.runId ?? ''}-${run.startedAt}`} run={run} />
      ))}
    </ol>
  );
}
