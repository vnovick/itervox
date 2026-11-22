import type { ReactNode } from 'react';
import { useIssueEvidence } from '../../../queries/evidence';
import type { EvidenceReport } from '../../../types/schemas';
import { EvidenceChecks } from './EvidenceChecks';
import { EvidenceRuns } from './EvidenceRuns';
import { approvalSubject, approver, decisionPill, evidenceTime } from './evidenceFormat';

function Section({
  title,
  count,
  children,
}: {
  title: string;
  count?: number;
  children: ReactNode;
}) {
  return (
    <section aria-label={title} className="glass-card rounded-[18px] px-3.5 py-3">
      <h4 className="text-theme-text mb-2 text-[13px] font-semibold">
        {title}
        {count !== undefined && (
          <span className="text-theme-muted ml-1.5 font-normal">{count}</span>
        )}
      </h4>
      {children}
    </section>
  );
}

function Approvals({ approvals }: Pick<EvidenceReport, 'approvals'>) {
  if (approvals.length === 0) {
    return <p className="text-theme-muted text-xs">No approvals or answers recorded.</p>;
  }
  return (
    <ul className="space-y-1 text-xs">
      {approvals.map((a) => {
        const who = approver(a);
        return (
          <li key={`${a.at}-${a.kind}-${a.decision}`} data-testid="evidence-approval">
            <span className="text-theme-text font-medium">{approvalSubject(a)}</span>{' '}
            <span className={`pill pill-sm ${decisionPill(a.decision)}`}>{a.decision}</span>{' '}
            <span className="text-theme-muted">
              {[evidenceTime(a.at), a.via, who].filter(Boolean).join(' · ')}
              {a.pathsTotal ? ` · ${String(a.pathsTotal)} files` : ''}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

function PullRequests({ pullRequests }: Pick<EvidenceReport, 'pullRequests'>) {
  if (pullRequests.length === 0) {
    return <p className="text-theme-muted text-xs">No pull request recorded.</p>;
  }
  return (
    <ul className="space-y-1 text-xs">
      {pullRequests.map((pr) => (
        <li key={pr.url} data-testid="evidence-pr">
          <a
            href={pr.url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-theme-accent-text hover:underline"
          >
            {pr.url}
          </a>{' '}
          <span className={`pill pill-sm ${pr.state === 'merged' ? 'pill-ok' : 'pill-neutral'}`}>
            {pr.state}
          </span>
        </li>
      ))}
    </ul>
  );
}

/**
 * The issue's evidence report (lazy tab): what ran, what was checked, who
 * approved, and the PRs. Read-only; the daemon has redacted it for the
 * caller's token (below operator: no steering text, no approvers).
 */
export default function IssueEvidenceTab({
  identifier,
  live,
}: {
  identifier: string;
  live: boolean;
}) {
  const { data, isPending, isError } = useIssueEvidence(identifier, live);
  if (isPending) return <p className="text-theme-muted text-sm">Loading evidence…</p>;
  if (isError) {
    return <p className="text-theme-danger-text text-sm">The evidence report could not be read.</p>;
  }
  if (!data) {
    return <p className="text-theme-muted text-sm">This daemon does not serve evidence reports.</p>;
  }
  const since = data.limits?.feedSince;
  return (
    <div className="space-y-3" data-testid="issue-evidence">
      <Section title="What ran" count={data.runs.length}>
        <EvidenceRuns runs={data.runs} />
        {data.retry && (
          <p className="text-theme-muted mt-2 text-xs">
            Retry {data.retry.attempt} due {evidenceTime(data.retry.dueAt)}
          </p>
        )}
      </Section>
      <Section title="What was checked">
        <EvidenceChecks
          signedChecks={data.signedChecks}
          reviews={data.reviews}
          guardrailBlocks={data.guardrailBlocks}
          pushLane={data.pushLane}
        />
      </Section>
      <Section title="Who approved" count={data.approvals.length}>
        <Approvals approvals={data.approvals} />
      </Section>
      <Section title="Pull requests" count={data.pullRequests.length}>
        <PullRequests pullRequests={data.pullRequests} />
      </Section>
      <p className="text-theme-muted text-[11px]">
        {since ? `Approvals, refusals and merges since ${evidenceTime(since)} UTC. ` : ''}
        Shown as a {data.scope} sees it. <code>itervox evidence {identifier} --json</code> prints
        the same report.
      </p>
    </div>
  );
}
