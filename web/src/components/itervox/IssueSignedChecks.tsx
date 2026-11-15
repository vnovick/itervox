import { useIssueAttestations } from '../../queries/attestations';
import type { AttestationRecord, AttestationRun } from '../../types/schemas';
import { checkDuration, checkOutcome, shortTree } from '../../utils/signedChecks';

/** A run's label: its profile and when its first check started (UTC). */
function runLabel(run: AttestationRun): string {
  const started = run.records.find((r) => r.startedAt)?.startedAt;
  const d = started ? new Date(started) : null;
  const when =
    d && !Number.isNaN(d.getTime()) ? d.toISOString().slice(0, 16).replace('T', ' ') : '';
  return [run.profile, when].filter(Boolean).join(' · ') || run.runId;
}

function VerifiedBadge({ verified, reason }: { verified: boolean; reason?: string }) {
  return verified ? (
    <span className="pill pill-ok pill-sm" title="Signature valid; ran on the run's final tree">
      verified
    </span>
  ) : (
    <span className="pill pill-danger pill-sm" title={reason}>
      not verified
    </span>
  );
}

function SealStatus({ run }: { run: AttestationRun }) {
  const seal = run.records.find((r) => r.kind === 'seal');
  if (!seal) return <span className="pill pill-warn pill-sm">not sealed</span>;
  return (
    <span className="flex items-center gap-1.5">
      <span className="text-theme-muted font-mono">sealed {shortTree(seal.treeHash)}</span>
      <VerifiedBadge verified={seal.verified} reason={seal.reason} />
    </span>
  );
}

function CheckRow({ record }: { record: AttestationRecord }) {
  const outcome = checkOutcome(record);
  return (
    <li className="text-xs" data-testid="signed-check">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-theme-text font-medium" title={record.command}>
          {record.name ?? record.command ?? 'check'}
        </span>
        <span className={`pill pill-sm ${outcome.ok ? 'pill-ok' : 'pill-danger'}`}>
          {outcome.label}
        </span>
        <span className="text-theme-muted">{checkDuration(record.durationMs)}</span>
        <span className="text-theme-muted font-mono" title={record.treeHash}>
          tree {shortTree(record.treeHash)}
        </span>
        <VerifiedBadge verified={record.verified} reason={record.reason} />
      </div>
      {!record.verified && record.reason && (
        <p className="text-theme-danger-text mt-0.5">{record.reason}</p>
      )}
      {record.error && <p className="text-theme-muted mt-0.5">{record.error}</p>}
    </li>
  );
}

/**
 * Signed checks (0.3.3) — every run's signed check records for the issue
 * (agent.profiles.<name>.verify), oldest run first: each check's outcome,
 * duration and tree, and whether its signature and the run's seal verify.
 * Read-only. Renders nothing while loading, on a read error, or when the
 * daemon does not serve signed checks; an empty state when no run has any.
 */
export function IssueSignedChecks({ identifier, live }: { identifier: string; live: boolean }) {
  const { data } = useIssueAttestations(identifier, live);
  if (!data) return null;
  return (
    <section
      data-testid="signed-checks"
      aria-label="Signed checks"
      className="glass-card rounded-[18px] px-3.5 py-3"
    >
      <h4 className="text-theme-text mb-2 flex items-baseline justify-between gap-2 text-[13px] font-semibold">
        Signed checks
        {data.keyId && (
          <span className="text-theme-muted font-mono text-[11px] font-normal">
            key {data.keyId}
          </span>
        )}
      </h4>
      {data.runs.length === 0 ? (
        <p className="text-theme-muted text-xs">No signed checks have run for this issue.</p>
      ) : (
        <ol className="space-y-3">
          {data.runs.map((run) => (
            <li key={run.runId} data-testid="signed-checks-run">
              <div className="mb-1 flex flex-wrap items-center justify-between gap-2 text-[11px]">
                <span className="text-theme-muted font-medium">{runLabel(run)}</span>
                <SealStatus run={run} />
              </div>
              <ul className="border-theme-line space-y-2 border-l pl-3">
                {run.records
                  .filter((r) => r.kind !== 'seal')
                  .map((r) => (
                    <CheckRow
                      key={`${run.runId}-${r.name ?? ''}-${r.startedAt ?? ''}`}
                      record={r}
                    />
                  ))}
              </ul>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
