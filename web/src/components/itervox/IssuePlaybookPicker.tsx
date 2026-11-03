import { useSetIssuePlaybook } from '../../queries/issues';
import { usePlaybooks } from '../../queries/playbooks';
import type { TrackerIssue } from '../../types/schemas';
import { RequireScope } from './RequireScope';

/** The select value for "no override" (automatic choice). */
const AUTO = '';
/** The override that attaches no playbook. */
const NONE = 'none';

const SOURCE_LABEL: Record<string, string> = {
  override: 'chosen here',
  label: 'from a label',
  keyword: 'from the title and description',
};

/** One line saying which playbook the issue's run gets and why. */
function playbookSummary(
  issue: Pick<TrackerIssue, 'playbook' | 'playbookSource' | 'playbookOverride'>,
): string {
  if (issue.playbookOverride === NONE) return 'No playbook (chosen here).';
  if (!issue.playbook) return 'No playbook matched — the run gets none.';
  const why = SOURCE_LABEL[issue.playbookSource ?? ''] ?? 'automatic';
  return `Runs with the ${issue.playbook} playbook (${why}).`;
}

/**
 * #218 — the issue's task-type playbook: Automatic (label, then keyword
 * classifier), None, or a playbook on disk. The override is event-loop
 * state on the daemon; the next run picks it up. Changing it needs the
 * operator scope; viewers see the summary only.
 */
export function IssuePlaybookPicker({ issue }: { issue: TrackerIssue }) {
  const { data } = usePlaybooks();
  const setPlaybook = useSetIssuePlaybook();
  const playbooks = data?.playbooks ?? [];
  if (!data || !data.enabled || playbooks.length === 0) return null;
  const value = issue.playbookOverride ?? AUTO;

  return (
    <div data-testid="issue-playbook">
      <h4 className="mb-1 text-xs font-medium tracking-wider uppercase">Playbook</h4>
      <RequireScope scope="operator">
        <select
          aria-label="Playbook"
          value={value}
          disabled={setPlaybook.isPending}
          onChange={(e) => {
            setPlaybook.mutate({ identifier: issue.identifier, playbook: e.target.value });
          }}
          className="focus:border-theme-accent border-theme-line bg-theme-panel-strong text-theme-text min-w-[160px] cursor-pointer rounded-[var(--radius-sm)] border px-3 py-2 text-[13px] focus:outline-none"
        >
          <option value={AUTO}>Automatic</option>
          <option value={NONE}>None</option>
          {playbooks.map((p) => (
            <option key={p.name} value={p.name} title={p.description}>
              {p.name}
            </option>
          ))}
        </select>
      </RequireScope>
      <p data-testid="issue-playbook-summary" className="text-theme-muted mt-1 text-[11px]">
        {playbookSummary(issue)}
      </p>
    </div>
  );
}
