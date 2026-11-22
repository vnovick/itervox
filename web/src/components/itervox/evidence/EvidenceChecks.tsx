import type { EvidenceReport } from '../../../types/schemas';
import { checkDuration } from '../../../utils/signedChecks';
import { evidenceTime } from './evidenceFormat';

type Props = Pick<EvidenceReport, 'signedChecks' | 'reviews' | 'guardrailBlocks' | 'pushLane'>;

function laneFiles(total: number, kept?: number): string {
  const files = `${String(total)} file${total === 1 ? '' : 's'}`;
  return kept === undefined ? files : `${files} (${String(kept)} kept)`;
}

function okPill(ok: boolean, yes: string, no: string) {
  return <span className={`pill pill-sm ${ok ? 'pill-ok' : 'pill-danger'}`}>{ok ? yes : no}</span>;
}

/**
 * What was checked: signed checks (names, exit codes, verification), the
 * reviewer verdicts and quorum (counts, never their text), guardrail
 * refusals and the push lane (how many files, never the paths).
 */
export function EvidenceChecks({ signedChecks, reviews, guardrailBlocks, pushLane }: Props) {
  const empty =
    signedChecks.length === 0 &&
    reviews.verdicts.length === 0 &&
    !reviews.outcome &&
    guardrailBlocks.length === 0 &&
    !pushLane;
  if (empty) {
    return <p className="text-theme-muted text-xs">No checks recorded for this issue.</p>;
  }
  return (
    <ul className="space-y-2 text-xs">
      {signedChecks.map((run) => (
        <li key={run.attestRunId} data-testid="evidence-signed">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-theme-text font-medium">Signed checks</span>
            <span className="text-theme-muted font-mono">{run.attestRunId}</span>
            {okPill(run.verified, 'verified', 'not verified')}
            {!run.sealed && <span className="pill pill-warn pill-sm">not sealed</span>}
          </div>
          <ul className="mt-0.5 space-y-0.5 pl-3">
            {run.checks.map((c) => (
              <li key={`${run.attestRunId}-${c.name}-${c.startedAt}`}>
                {c.name}{' '}
                {okPill(
                  c.passed,
                  'ok',
                  c.timedOut ? 'timed out' : `failed · exit ${String(c.exitCode)}`,
                )}{' '}
                <span className="text-theme-muted">{checkDuration(c.durationMs)}</span>
              </li>
            ))}
          </ul>
        </li>
      ))}
      {reviews.verdicts.map((v) => (
        <li key={`${v.profile}-${v.recordedAt}`} data-testid="evidence-verdict">
          <span className="text-theme-text font-medium">Review</span> {v.profile}{' '}
          {okPill(v.verdict === 'approve', v.verdict, v.verdict)}{' '}
          <span className="text-theme-muted">
            {[v.backend, `${String(v.reasons)} reasons`, `${String(v.comments)} line comments`]
              .filter(Boolean)
              .join(' · ')}
          </span>
        </li>
      ))}
      {reviews.outcome && (
        <li data-testid="evidence-quorum">
          <span className="text-theme-text font-medium">Review quorum</span>{' '}
          {reviews.outcome.quorum ?? ''} {okPill(!reviews.outcome.blocked, 'passed', 'blocked')}{' '}
          <span className="text-theme-muted">
            {reviews.outcome.approvals} approve · {reviews.outcome.blocks} block
          </span>
        </li>
      )}
      {guardrailBlocks.map((g) => (
        <li key={`${g.at}-${g.kind}`} data-testid="evidence-guardrail-block">
          <span className="text-theme-text font-medium">Guardrail refused</span>{' '}
          <span className="font-mono">{g.kind}</span>{' '}
          <span className="text-theme-muted">{evidenceTime(g.at)}</span>
        </li>
      ))}
      {pushLane && (
        <li data-testid="evidence-push-lane">
          <span className="text-theme-text font-medium">Push lane</span>{' '}
          <span className="pill pill-warn pill-sm">{pushLane.lane}</span>{' '}
          {laneFiles(pushLane.pathsTotal, pushLane.truncated ? pushLane.pathsKept : undefined)}
          {pushLane.writerBackends.length > 0 && (
            <span className="text-theme-muted">
              {' '}
              · written on {pushLane.writerBackends.join(', ')}
            </span>
          )}
        </li>
      )}
    </ul>
  );
}
