import type { BlockerDetail, TrackerIssue } from '../../types/schemas';

interface IssueBlockerDetailsProps {
  issue: Pick<TrackerIssue, 'blockedBy' | 'blockedByDetails' | 'ineligibleReason'>;
}

export function IssueBlockerDetails({ issue }: IssueBlockerDetailsProps) {
  const blockers = blockersForIssue(issue);
  if (!issue.ineligibleReason && blockers.length === 0) return null;

  return (
    <>
      {issue.ineligibleReason && (
        <div>
          <h4 className="mb-1 text-xs font-medium tracking-wider uppercase">Not dispatchable</h4>
          <p className="bg-theme-warning-soft text-theme-warning inline-flex rounded px-2 py-1 font-mono text-xs">
            {issue.ineligibleReason}
          </p>
        </div>
      )}

      {blockers.length > 0 && (
        <div>
          <h4 className="mb-1 text-xs font-medium tracking-wider uppercase">Blocked by</h4>
          <div className="flex flex-wrap gap-1.5">
            {blockers.map((blocker) => (
              <span
                key={blocker.identifier}
                className="bg-theme-danger-soft text-theme-danger inline-flex items-center gap-1.5 rounded px-2 py-0.5 text-xs"
              >
                {blocker.url ? (
                  <a
                    href={blocker.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="font-mono hover:underline"
                  >
                    {blocker.identifier}
                  </a>
                ) : (
                  <span className="font-mono">{blocker.identifier}</span>
                )}
                {blocker.state && (
                  <span className="bg-theme-bg-soft text-theme-text-secondary rounded px-1.5 py-0.5 text-[10px] font-medium">
                    {blocker.state}
                  </span>
                )}
              </span>
            ))}
          </div>
        </div>
      )}
    </>
  );
}

function blockersForIssue(
  issue: Pick<TrackerIssue, 'blockedBy' | 'blockedByDetails'>,
): BlockerDetail[] {
  if (issue.blockedByDetails && issue.blockedByDetails.length > 0) {
    return issue.blockedByDetails;
  }
  return (issue.blockedBy ?? []).map((identifier) => ({ identifier }));
}
