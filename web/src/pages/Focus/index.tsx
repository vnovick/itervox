import { lazy, Suspense } from 'react';
import { Link } from 'react-router';
import { useItervoxStore } from '../../store/itervoxStore';
import { useIssue, useIssues } from '../../queries/issues';
import { RUN_CONTEXT_CAPABILITY } from '../../queries/changes';
import { useFocusIssue } from '../../hooks/useFocusIssue';
import { EmptyState } from '../../components/ui/EmptyState';
import { plainButtonClass } from '../../components/ui/button/buttonRecipe';
import { FocusNowPanel } from './FocusNowPanel';

// The diff viewer is the same lazy chunk the issue detail's Changes tab loads.
const IssueChangesTab = lazy(() => import('../../components/itervox/changes/IssueChangesTab'));

const BOARD_LINK = plainButtonClass('secondary', 'md', { pill: true });

/**
 * The issue view: one issue, what its agent is doing now on the left and
 * its changes on the right (stacked at phone width). `/focus` follows the
 * issue that needs the operator most (see pickFocusIssue); `/focus/:id`
 * pins one. The desktop app's window lands here.
 */
export default function FocusPage() {
  const focus = useFocusIssue();
  const identifier = focus?.identifier ?? '';
  const hasSnapshot = useItervoxStore((s) => s.snapshot !== null);
  const runContext = useItervoxStore(
    (s) => s.snapshot?.capabilities?.includes(RUN_CONTEXT_CAPABILITY) ?? false,
  );
  const { data: issues, isSuccess: listLoaded } = useIssues();
  const { data: fresh, isError: issueMissing } = useIssue(identifier);
  const issue = fresh ?? issues?.find((i) => i.identifier === identifier) ?? null;

  const heading = <h1 className="sr-only">Issue view</h1>;

  if (!focus) {
    return (
      <div className="glass-panel flex min-h-[320px] flex-col" data-testid="focus-empty">
        {heading}
        <EmptyState
          title={hasSnapshot ? 'Nothing to focus on yet' : 'Loading…'}
          description={
            hasSnapshot
              ? 'An issue shows up here when its agent needs your input, is running, or has just finished.'
              : undefined
          }
          action={
            hasSnapshot ? (
              <Link to="/" className={BOARD_LINK}>
                Open the board
              </Link>
            ) : undefined
          }
        />
      </div>
    );
  }

  if (!issue) {
    const missing = issueMissing && listLoaded;
    return (
      <div className="glass-panel flex min-h-[320px] flex-col" data-testid="focus-missing">
        {heading}
        <EmptyState
          title={missing ? `${identifier} was not found` : `Loading ${identifier}…`}
          action={
            missing ? (
              <Link to="/" className={BOARD_LINK}>
                Open the board
              </Link>
            ) : undefined
          }
        />
      </div>
    );
  }

  const live = issue.orchestratorState === 'running';
  return (
    <div
      data-testid="focus-page"
      data-identifier={issue.identifier}
      className="grid min-w-0 grid-cols-1 gap-4 lg:min-h-[calc(100dvh-112px)] lg:grid-cols-2"
    >
      {heading}
      <FocusNowPanel key={issue.identifier} issue={issue} runContext={runContext} />
      <section
        aria-labelledby="focus-changes-heading"
        data-testid="focus-changes"
        className="glass-panel flex min-w-0 flex-col gap-3 px-5 py-[18px]"
      >
        <h2 id="focus-changes-heading" className="section-title">
          Changes
        </h2>
        {runContext ? (
          <Suspense fallback={<p className="text-theme-muted text-sm">Loading changes…</p>}>
            <IssueChangesTab
              key={issue.identifier}
              identifier={issue.identifier}
              live={live}
              issueState={issue.state}
            />
          </Suspense>
        ) : (
          <p className="text-theme-muted text-sm italic">This daemon does not serve issue diffs.</p>
        )}
      </section>
    </div>
  );
}
