import { lazy, Suspense, useCallback, useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import MarkdownPanel from './MarkdownPanel';
import IssueDetailHeader, { IssueDetailBadges } from './IssueDetailHeader';
import { IssueBlockerDetails } from './IssueBlockerDetails';
import { IssueReviewThread } from './IssueReviewThread';
import { IssueStatusChanges } from './IssueStatusChanges';
import { IssueReviews } from './IssueReviews';
import { InputRequiredPanel } from './InputRequiredPanel';
import { IssueCommentComposer } from './IssueCommentComposer';
import { IssueBackendControl } from './IssueBackendControl';
import { IssueProfilePicker } from './IssueProfilePicker';
import { IssuePlaybookPicker } from './IssuePlaybookPicker';
import { IssueSteerBox } from './IssueSteerBox';
import { DecisionsTimeline } from './DecisionsTimeline';
import { useItervoxStore } from '../../store/itervoxStore';
import { SlidePanel } from '../ui/SlidePanel/SlidePanel';
import { ConfirmButton } from '../ui/button/ConfirmButton';
import {
  useIssues,
  useIssue,
  useCancelIssue,
  useResumeIssue,
  useTerminateIssue,
  useProvideInput,
  useDismissInput,
  useTriggerAIReview,
  ISSUES_KEY,
  ISSUE_KEY,
} from '../../queries/issues';
import { EMPTY_DEPENDENCY_ATTENTION, EMPTY_STATES } from '../../utils/constants';
import { RUN_CONTEXT_CAPABILITY } from '../../queries/changes';
import { IssueNowPanel } from './changes/IssueNowPanel';
import { IssueDetailTabs, type IssueDetailTab } from './changes/IssueDetailTabs';
import { plainButtonClass } from '../ui/button/buttonRecipe';
import { focusPathFor } from '../../hooks/useFocusIssue';
import { Link } from 'react-router';

// #215 — the diff viewer is only loaded when the Changes tab opens.
const IssueChangesTab = lazy(() => import('./changes/IssueChangesTab'));
// #251: only a New task has instructions; kept out of the initial bundle.
const IssueTaskInstructions = lazy(() => import('./IssueTaskInstructions'));

// CORE-070 — tracker.working_state default (internal/config/config.go), used
// only when the snapshot does not publish workingState (older daemons).
export const DEFAULT_WORKING_STATE = 'In Progress';

// Plain recipe classes: this slide loads with the app shell.
const SMALL_BUTTON = plainButtonClass('secondary', 'sm', { pill: true });
const FOOTER_BUTTON = plainButtonClass('secondary', 'lg', { pill: true, extra: 'min-h-11' });

export default function IssueDetailSlide() {
  const selectedIdentifier = useItervoxStore((s) => s.selectedIdentifier);
  const setSelectedIdentifier = useItervoxStore((s) => s.setSelectedIdentifier);
  const queryClient = useQueryClient();

  const { data: issuesList = [] } = useIssues();
  const { data: freshIssue } = useIssue(selectedIdentifier ?? '');
  const issue = freshIssue ?? issuesList.find((i) => i.identifier === selectedIdentifier) ?? null;

  // CORE-073 — one verb per action, matching the dashboard tables and the TUI
  // CORE-073 (spec): Discard for /terminate on running and paused issues alike.
  const cancelIssueMutation = useCancelIssue();
  const cancelRetryMutation = useCancelIssue({ verb: 'Cancel retry' });
  const terminateIssueMutation = useTerminateIssue();
  const resumeIssueMutation = useResumeIssue();
  const provideInputMutation = useProvideInput();
  const dismissInputMutation = useDismissInput();
  const triggerAIReviewMutation = useTriggerAIReview();
  const reviewerProfile = useItervoxStore((s) => s.snapshot?.reviewerProfile ?? '');
  const defaultBackend = useItervoxStore((s) => s.snapshot?.defaultBackend ?? 'claude');
  const profileDefs = useItervoxStore((s) => s.snapshot?.profileDefs);
  const runningRows = useItervoxStore((s) => s.snapshot?.running);
  const historyRows = useItervoxStore((s) => s.snapshot?.history);
  const automations = useItervoxStore((s) => s.snapshot?.automations);
  // critical-path-ordering Task 5/6 — cycle/stale-blocker alerts, derived
  // event-loop-side and surfaced read-only on the snapshot.
  const dependencyAttention = useItervoxStore(
    (s) => s.snapshot?.dependencyAttention ?? EMPTY_DEPENDENCY_ATTENTION,
  );
  // outbox #54 fast-follow: same join-by-identifier against
  // snapshot.outboxSyncing BoardView's DraggableCard has used since Task 4.
  const outboxSyncing = useItervoxStore((s) => s.snapshot?.outboxSyncing ?? EMPTY_STATES);
  const inlineInput = useItervoxStore((s) => s.snapshot?.inlineInput ?? false);
  const workingState = useItervoxStore((s) => s.snapshot?.workingState || DEFAULT_WORKING_STATE);
  const runContextSupported = useItervoxStore(
    (s) => s.snapshot?.capabilities?.includes(RUN_CONTEXT_CAPABILITY) ?? false,
  );
  // Every issue opens on its details: the chosen tab is remembered per
  // identifier, not carried over to the next issue.
  const [tabFor, setTabFor] = useState<{ id: string | null; tab: IssueDetailTab }>({
    id: null,
    tab: 'details',
  });
  const tab = tabFor.id === selectedIdentifier ? tabFor.tab : 'details';
  const setTab = useCallback(
    (next: IssueDetailTab) => {
      setTabFor({ id: selectedIdentifier, tab: next });
    },
    [selectedIdentifier],
  );

  const close = useCallback(() => {
    setSelectedIdentifier(null);
  }, [setSelectedIdentifier]);

  // Invalidate issues cache when the slide opens so comments/branch info are fresh.
  useEffect(() => {
    if (selectedIdentifier) {
      void queryClient.invalidateQueries({ queryKey: ISSUES_KEY });
      void queryClient.invalidateQueries({ queryKey: ISSUE_KEY(selectedIdentifier) });
    }
  }, [selectedIdentifier, queryClient]);

  if (!selectedIdentifier || !issue) return null;

  const issueAttention = dependencyAttention.find((row) => row.identifier === issue.identifier);
  // CORE-070 — the lock follows the configured working state (exact,
  // case-insensitive match), not any state name containing "progress".
  const isProfileLocked =
    issue.orchestratorState === 'running' ||
    issue.state.toLowerCase() === workingState.toLowerCase();
  // The footer carries run controls only. Review is rendered once, in the
  // body (reachable for every non-running issue, including idle backlog and
  // completion-state issues), so the footer no longer keys on a state name.
  const hasFooterActions =
    issue.orchestratorState === 'running' ||
    issue.orchestratorState === 'retrying' ||
    issue.orchestratorState === 'paused';

  const headerProps = {
    issue,
    runningRows,
    history: historyRows,
    profileDefs,
    defaultBackend,
    automations,
    syncing: outboxSyncing.includes(issue.identifier),
  };

  return (
    <SlidePanel
      isOpen
      direction="right"
      size="medium"
      monoTitle
      title={issue.identifier}
      onClose={close}
      titleAccessory={<IssueDetailBadges {...headerProps} />}
    >
      {/* Title on its own line; the badges sit inline after the identifier. */}
      <IssueDetailHeader {...headerProps} badges={false} />

      {runContextSupported && <IssueDetailTabs tab={tab} onChange={setTab} />}

      {runContextSupported && tab === 'changes' ? (
        <div
          className="flex-1 overflow-y-auto px-6 py-4"
          role="tabpanel"
          aria-labelledby="issue-tab-changes"
        >
          <Suspense fallback={<p className="text-theme-muted text-sm">Loading changes…</p>}>
            <IssueChangesTab
              identifier={issue.identifier}
              live={issue.orchestratorState === 'running'}
            />
          </Suspense>
        </div>
      ) : (
        /* Scrollable body */
        <div className="flex-1 space-y-4 overflow-y-auto px-6 pt-3 pb-5">
          {runContextSupported && (
            <IssueNowPanel
              identifier={issue.identifier}
              live={issue.orchestratorState === 'running'}
            />
          )}
          {runContextSupported && issue.orchestratorState === 'running' && (
            <IssueSteerBox identifier={issue.identifier} /> // #218
          )}
          <div className="flex items-center gap-3">
            <Link to={focusPathFor(issue.identifier)} className={SMALL_BUTTON}>
              Open full view
            </Link>
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
            {reviewerProfile && issue.orchestratorState !== 'running' && (
              <button
                onClick={() => {
                  triggerAIReviewMutation.mutate(issue.identifier);
                }}
                disabled={triggerAIReviewMutation.isPending}
                className={SMALL_BUTTON}
                title={`Dispatch reviewer (${reviewerProfile} profile)`}
              >
                {triggerAIReviewMutation.isPending ? 'Reviewing…' : '🔍 Review'}
              </button>
            )}
          </div>
          {/* Status changes beside decisions; either takes the full width
              when the other has nothing to show. */}
          <div className="flex flex-col gap-3 empty:hidden sm:flex-row sm:items-start [&>*]:min-w-0 [&>*]:flex-1">
            <IssueStatusChanges changes={issue.statusChanges} />
            <DecisionsTimeline
              identifier={issue.identifier}
              live={issue.orchestratorState === 'running'}
            />
          </div>
          <IssueReviews
            review={issue.review}
            rerunPending={triggerAIReviewMutation.isPending}
            onRerun={
              reviewerProfile && issue.orchestratorState !== 'running'
                ? () => {
                    triggerAIReviewMutation.mutate(issue.identifier);
                  }
                : undefined
            }
          />
          {/* Priority + Labels */}
          {(issue.priority != null || (issue.labels && issue.labels.length > 0)) && (
            <div className="flex flex-wrap items-center gap-2">
              {issue.priority != null && (
                <span className="pill pill-neutral py-0.5 text-xs">P{issue.priority}</span>
              )}
              {issue.labels?.map((label) => (
                <span key={label} className="pill pill-neutral py-0.5 text-xs">
                  {label}
                </span>
              ))}
            </div>
          )}
          {/* Agent profile (backend-aware, #123), playbook (#218) and backend
              (CORE-056): one row of equal selects on wider sheets. */}
          <div className="grid items-start gap-3 sm:grid-cols-3">
            <IssueProfilePicker issue={issue} locked={isProfileLocked} />
            <IssuePlaybookPicker issue={issue} />
            <IssueBackendControl
              key={issue.identifier} // BH-M3-10: no error/pending state across issues
              issue={issue}
              defaultBackend={defaultBackend}
            />
          </div>
          {issue.task && (
            <Suspense fallback={null}>
              <IssueTaskInstructions issue={issue} />
            </Suspense>
          )}
          {/* Branch */}
          {issue.branchName && (
            <div className="flex flex-wrap items-center gap-2">
              <h4 className="field-label">Branch</h4>
              <code className="glass-chip text-theme-text rounded-lg px-2 py-1 font-mono text-xs">
                {issue.branchName}
              </code>
              <button
                type="button"
                onClick={() => {
                  void navigator.clipboard.writeText(issue.branchName ?? '').catch(() => {});
                }}
                className={SMALL_BUTTON}
                title="Copy branch name"
              >
                Copy
              </button>
            </div>
          )}
          <IssueBlockerDetails issue={issue} attention={issueAttention} />
          {/* Description */}
          <div>
            <h4 className="field-label mb-1.5">Description</h4>
            {issue.description ? (
              <MarkdownPanel>{issue.description}</MarkdownPanel>
            ) : (
              <p className="text-theme-muted text-sm italic">No description</p>
            )}
          </div>
          {/* Review thread (T-7) — agent-marked thread sits between description
            and the heavier "Comments" block so an operator can scan agent
            review activity without scrolling through every human comment. */}
          {issue.comments && issue.comments.length > 0 && (
            <IssueReviewThread comments={issue.comments} />
          )}
          {/* Comments — always rendered oldest-first so the thread reads in
            chronological order and matching tracker UI conventions. The
            backend contract (domain.Issue.Comments) guarantees ascending
            CreatedAt but tracker adapters occasionally disagree; sort
            defensively in the UI so the UX is stable regardless. */}
          {issue.comments &&
            issue.comments.length > 0 &&
            (() => {
              const sortedComments = [...issue.comments].sort((a, b) => {
                const at = a.createdAt ? new Date(a.createdAt).getTime() : 0;
                const bt = b.createdAt ? new Date(b.createdAt).getTime() : 0;
                return at - bt;
              });
              // Identify the Itervox bot's latest question comment so the UI
              // can mark it inline with a small "agent question" pill when the
              // issue is in input_required or pending_input_resume state —
              // answering the "which comment triggered the banner?" question.
              const isBotAuthor = (author?: string) =>
                !!author && author.trim().toLowerCase() === 'itervox';
              const latestBotCommentIdx = (() => {
                for (let i = sortedComments.length - 1; i >= 0; i--) {
                  if (isBotAuthor(sortedComments[i].author)) return i;
                }
                return -1;
              })();
              const showQuestionMarker =
                issue.orchestratorState === 'input_required' ||
                issue.orchestratorState === 'pending_input_resume';
              return (
                <div>
                  <h4 className="text-theme-text mb-2 text-[13px] font-semibold">
                    Comments ({sortedComments.length})
                  </h4>
                  <div className="space-y-2">
                    {sortedComments.map((c, i) => (
                      <div
                        key={`${c.author}-${c.createdAt ?? String(i)}`}
                        className="glass-card space-y-1.5 px-3 py-2.5 text-[13px]"
                      >
                        <div className="flex items-center gap-2">
                          <span className="bg-theme-accent flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full text-[10px] font-bold text-white">
                            {(c.author || '?').charAt(0).toUpperCase()}
                          </span>
                          <span className="text-theme-text text-[13px] font-semibold">
                            {c.author || 'Unknown'}
                          </span>
                          {showQuestionMarker && i === latestBotCommentIdx && (
                            <span className="tag pill-warn">
                              {issue.orchestratorState === 'input_required'
                                ? 'agent question'
                                : 'reply received'}
                            </span>
                          )}
                          {c.createdAt && (
                            <span className="text-theme-muted ml-auto text-xs">
                              {new Date(c.createdAt).toLocaleDateString(undefined, {
                                month: 'short',
                                day: 'numeric',
                                year: 'numeric',
                              })}
                            </span>
                          )}
                        </div>
                        <MarkdownPanel>{c.body}</MarkdownPanel>
                      </div>
                    ))}
                  </div>
                </div>
              );
            })()}
          {/* Operator comment composer — always available except while the
            issue is input_required, where the reply box below is the
            answer channel for the agent's question instead. */}
          {issue.orchestratorState !== 'input_required' && (
            <IssueCommentComposer identifier={issue.identifier} />
          )}
          {/* Input Required — reply UI (extracted, Task 5 size-budget) */}
          <InputRequiredPanel
            issue={issue}
            inlineInput={inlineInput}
            provideInputMutation={provideInputMutation}
            dismissInputMutation={dismissInputMutation}
          />
        </div>
      )}

      {/* Sticky action footer — run controls (CORE-073 verbs) */}
      {hasFooterActions && (
        <div
          data-testid="issue-detail-footer"
          className="border-theme-line mx-6 flex flex-shrink-0 items-center gap-3 border-t py-3.5"
        >
          <div className="flex items-center gap-2">
            {/* Paused state */}
            {issue.orchestratorState === 'paused' && (
              <>
                <button
                  onClick={() => {
                    // Close only once the daemon accepted the resume; on
                    // failure the slide stays open with the error toast so
                    // the operator can retry.
                    resumeIssueMutation.mutate(issue.identifier, { onSuccess: close });
                  }}
                  disabled={resumeIssueMutation.isPending}
                  className={FOOTER_BUTTON}
                >
                  {resumeIssueMutation.isPending ? 'Resuming…' : '▶ Resume'}
                </button>
                <ConfirmButton
                  size="lg"
                  label="✕ Discard"
                  confirmLabel="Yes, discard"
                  pendingLabel="Discarding…"
                  isPending={terminateIssueMutation.isPending}
                  onConfirm={() => {
                    terminateIssueMutation.mutate(issue.identifier);
                  }}
                />
              </>
            )}

            {/* Running state */}
            {issue.orchestratorState === 'running' && (
              <>
                <button
                  onClick={() => {
                    cancelIssueMutation.mutate(issue.identifier);
                  }}
                  disabled={cancelIssueMutation.isPending || terminateIssueMutation.isPending}
                  className={FOOTER_BUTTON}
                >
                  {cancelIssueMutation.isPending ? 'Pausing…' : '⏸ Pause'}
                </button>
                <ConfirmButton
                  size="lg"
                  label="✕ Discard"
                  confirmLabel="Yes, discard"
                  pendingLabel="Discarding…"
                  isPending={cancelIssueMutation.isPending || terminateIssueMutation.isPending}
                  onConfirm={() => {
                    terminateIssueMutation.mutate(issue.identifier);
                  }}
                />
              </>
            )}

            {/* Retrying state */}
            {issue.orchestratorState === 'retrying' && (
              <button
                onClick={() => {
                  cancelRetryMutation.mutate(issue.identifier);
                }}
                disabled={cancelRetryMutation.isPending}
                className={FOOTER_BUTTON}
              >
                {cancelRetryMutation.isPending ? 'Cancelling retry…' : '✕ Cancel retry'}
              </button>
            )}
          </div>
        </div>
      )}
    </SlidePanel>
  );
}
