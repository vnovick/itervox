import type { IssueDetailTab } from './IssueDetailTabs';

/**
 * The issue detail's tabs after Details that the daemon serves: Changes
 * with the run_context capability (#215), Evidence with issue_evidence.
 */
export function issueDetailTabList(runContext: boolean, evidence: boolean): IssueDetailTab[] {
  const tabs: IssueDetailTab[] = [];
  if (runContext) tabs.push('changes');
  if (evidence) tabs.push('evidence');
  return tabs;
}
