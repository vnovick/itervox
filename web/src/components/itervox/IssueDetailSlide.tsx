import { useCallback, useEffect } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import MarkdownPanel from './MarkdownPanel';
import IssueDetailHeader from './IssueDetailHeader';
import { IssueBlockerDetails } from './IssueBlockerDetails';
import { IssueReviewThread } from './IssueReviewThread';
import { IssueStatusChanges } from './IssueStatusChanges';
import { InputRequiredPanel } from './InputRequiredPanel';
import { IssueCommentComposer } from './IssueCommentComposer';
import { IssueBackendControl } from './IssueBackendControl';
import { useItervoxStore } from '../../store/itervoxStore';
import { SlidePanel } from '../ui/SlidePanel/SlidePanel';
import { ConfirmButton } from '../ui/button/ConfirmButton';
import {
  useIssues,
  useIssue,
  useCancelIssue,
  useResumeIssue,
  useTerminateIssue,
  useSetIssueProfile,
  useProvideInput,
  useDismissInput,
  useTriggerAIReview,
  ISSUES_KEY,
  ISSUE_KEY,
} from '../../queries/issues';
import { EMPTY_PROFILE_LABEL, EMPTY_PROFILES } from '../../utils/format';
import { EMPTY_DEPENDENCY_ATTENTION, EMPTY_STATES } from '../../utils/constants';

// CORE-070 — tracker.working_state default (internal/config/config.go), used
// only when the snapshot does not publish workingState (older daemons).
export const DEFAULT_WORKING_STATE = 'In Progress';

export default function IssueDetailSlide() {
  const selectedIdentifier = useItervoxStore((s) => s.selectedIdentifier);
  const setSelectedIdentifier = useItervoxStore((s) => s.setSelectedIdentifier);
  const availableProfiles = useItervoxStore((s) => s.snapshot?.availableProfiles ?? EMPTY_PROFILES);
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
  const setIssueProfileMutation = useSetIssueProfile();
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

  return (
    <SlidePanel isOpen direction="right" title={issue.identifier} onClose={close}>
      {/* Sub-header: badges + title (extracted to IssueDetailHeader, T-20) */}
      <IssueDetailHeader
        issue={issue}
        runningRows={runningRows}
        history={historyRows}
        profileDefs={profileDefs}
        defaultBackend={defaultBackend}
        automations={automations}
        syncing={outboxSyncing.includes(issue.identifier)}
      />

      {/* Scrollable body */}
      <div className="flex-1 space-y-5 overflow-y-auto px-5 py-4">
        <div className="flex items-center gap-3">
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
              className="rounded-[var(--radius-sm)] px-2.5 py-1 text-xs font-medium transition-colors hover:opacity-80"
              style={{
                background: 'rgba(168,85,247,0.12)',
                borderColor: 'rgba(168,85,247,0.2)',
                color: 'rgb(168,85,247)',
              }}
              title={`Dispatch reviewer (${reviewerProfile} profile)`}
            >
              {triggerAIReviewMutation.isPending ? 'Reviewing…' : '🔍 Review'}
            </button>
          )}
        </div>

        <IssueStatusChanges changes={issue.statusChanges} />

        {/* Priority + Labels */}
        {(issue.priority != null || (issue.labels && issue.labels.length > 0)) && (
          <div className="flex flex-wrap items-center gap-2">
            {issue.priority != null && (
              <span className="bg-theme-warning-soft text-theme-warning-text inline-flex items-center rounded px-2 py-0.5 text-xs font-medium">
                P{issue.priority}
              </span>
            )}
            {issue.labels?.map((label) => (
              <span
                key={label}
                className="bg-theme-bg-soft text-theme-text-secondary inline-flex items-center rounded px-2 py-0.5 text-xs font-medium"
              >
                {label}
              </span>
            ))}
          </div>
        )}

        {/* Agent Profile */}
        {availableProfiles.length > 0 && (
          <div>
            <h4 className="mb-1 text-xs font-medium tracking-wider uppercase">Agent Profile</h4>
            {isProfileLocked ? (
              <div className="flex items-center gap-2">
                <span className="text-theme-text-secondary text-xs">
                  {issue.agentProfile ?? EMPTY_PROFILE_LABEL}
                </span>
                <span className="bg-theme-warning-soft text-theme-warning-text rounded px-1.5 py-0.5 text-[10px]">
                  locked while {issue.orchestratorState === 'running' ? 'running' : issue.state}
                </span>
              </div>
            ) : (
              <select
                aria-label="Agent profile"
                value={issue.agentProfile ?? ''}
                onChange={(e) => {
                  setIssueProfileMutation.mutate({
                    identifier: issue.identifier,
                    profile: e.target.value,
                  });
                }}
                className="focus:border-theme-accent rounded-[var(--radius-sm)] border px-3 py-2 text-[13px] focus:outline-none"
                style={{
                  borderColor: 'var(--line)',
                  background: 'var(--panel-strong)',
                  color: 'var(--text)',
                  cursor: 'pointer',
                  minWidth: '160px',
                }}
              >
                <option value="">{EMPTY_PROFILE_LABEL}</option>
                {availableProfiles.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            )}
          </div>
        )}

        {/* Agent Backend (CORE-056) */}
        <IssueBackendControl
          key={issue.identifier} // BH-M3-10: no error/pending state across issues
          issue={issue}
          defaultBackend={defaultBackend}
        />

        {/* Branch */}
        {issue.branchName && (
          <div>
            <h4 className="mb-1 text-xs font-medium tracking-wider uppercase">Branch</h4>
            <div className="flex items-center gap-2">
              <code className="bg-theme-bg-soft text-theme-text rounded px-2 py-1 font-mono text-xs">
                {issue.branchName}
              </code>
              <button
                onClick={() => {
                  void navigator.clipboard.writeText(issue.branchName ?? '').catch(() => {});
                }}
                className="text-xs"
                title="Copy branch name"
              >
                Copy
              </button>
            </div>
          </div>
        )}

        <IssueBlockerDetails issue={issue} attention={issueAttention} />

        {/* Description */}
        <div>
          <h4 className="mb-2 text-xs font-medium tracking-wider uppercase">Description</h4>
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
                <h4 className="mb-2 text-xs font-medium tracking-wider uppercase">
                  Comments ({sortedComments.length})
                </h4>
                <div className="space-y-4">
                  {sortedComments.map((c, i) => (
                    <div
                      key={`${c.author}-${c.createdAt ?? String(i)}`}
                      className="border-theme-line bg-theme-bg-soft space-y-2 rounded-lg border p-3"
                    >
                      <div className="flex items-center gap-2">
                        <span
                          className="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full text-[10px] font-bold text-white"
                          style={{ background: 'var(--gradient-accent)' }}
                        >
                          {(c.author || '?').charAt(0).toUpperCase()}
                        </span>
                        <span className="text-theme-text text-sm font-medium">
                          {c.author || 'Unknown'}
                        </span>
                        {showQuestionMarker && i === latestBotCommentIdx && (
                          <span className="rounded-full bg-orange-500/15 px-1.5 py-0.5 text-[10px] font-medium text-orange-400">
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

      {/* Sticky action footer — run controls (CORE-073 verbs) */}
      {hasFooterActions && (
        <div
          data-testid="issue-detail-footer"
          className="border-theme-line flex flex-shrink-0 items-center justify-end gap-3 border-t px-5 py-4"
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
                  className="bg-theme-success-soft text-theme-success-text rounded-lg px-4 py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50"
                >
                  {resumeIssueMutation.isPending ? 'Resuming…' : '▶ Resume'}
                </button>
                <ConfirmButton
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
                  className="bg-theme-warning-soft text-theme-warning-text rounded-lg px-4 py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50"
                >
                  {cancelIssueMutation.isPending ? 'Pausing…' : '⏸ Pause'}
                </button>
                <ConfirmButton
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
                className="bg-theme-warning-soft text-theme-warning-text rounded-lg px-4 py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50"
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
