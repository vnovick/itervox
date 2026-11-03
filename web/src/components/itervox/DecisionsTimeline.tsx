import { useIssueDecisions } from '../../queries/playbooks';
import type { DecisionRun } from '../../types/schemas';

/** A run's label: its role and when it started (from the file name). */
function runLabel(run: DecisionRun): string {
  const when = run.runTimestamp
    ? run.runTimestamp.replace(/T(\d\d)-(\d\d)-(\d\d).*$/, ' $1:$2')
    : '';
  return [run.role, when].filter(Boolean).join(' · ') || run.path;
}

/** A decision's time as written, shortened to HH:MM when it is RFC 3339. */
function decisionTime(time?: string, at?: string): string {
  if (at) {
    const d = new Date(at);
    if (!Number.isNaN(d.getTime())) return d.toISOString().slice(11, 16);
  }
  return time ?? '';
}

/**
 * #218 — the issue's decision timeline: every run's decisions.tsv (time,
 * decision, why, alternative), oldest run first, read from the worktree
 * (redacted and bounded by the daemon). Renders nothing when no run logged
 * a decision.
 */
export function DecisionsTimeline({ identifier, live }: { identifier: string; live: boolean }) {
  const { data } = useIssueDecisions(identifier, live);
  const runs = (data?.runs ?? []).filter((r) => r.decisions.length > 0);
  if (runs.length === 0) return null;
  return (
    <section data-testid="decisions-timeline" aria-label="Decisions">
      <h4 className="mb-2 text-xs font-medium tracking-wider uppercase">Decisions</h4>
      <ol className="space-y-3">
        {runs.map((run) => (
          <li key={run.path}>
            <p className="text-theme-muted mb-1 text-[11px] font-medium">
              {runLabel(run)}
              {run.truncated ? ' · truncated' : ''}
            </p>
            <ol className="border-theme-line space-y-2 border-l pl-3">
              {run.decisions.map((d) => (
                <li key={`${run.path}-${d.time ?? ''}-${d.decision}`} className="text-xs">
                  <span className="text-theme-muted mr-2 font-mono">
                    {decisionTime(d.time, d.at)}
                  </span>
                  <span className="text-theme-text font-medium">{d.decision}</span>
                  {d.why && <p className="text-theme-text-secondary">Why: {d.why}</p>}
                  {d.alternative && <p className="text-theme-muted">Instead of: {d.alternative}</p>}
                </li>
              ))}
            </ol>
          </li>
        ))}
      </ol>
    </section>
  );
}
