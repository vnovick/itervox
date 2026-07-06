import { useCallback, useEffect, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import MarkdownPanel from './MarkdownPanel';
import IssueDetailHeader from './IssueDetailHeader';
import { IssueBlockerDetails } from './IssueBlockerDetails';
import { IssueReviewThread } from './IssueReviewThread';
import { IssueStatusChanges } from './IssueStatusChanges';
import { useItervoxStore } from '../../store/itervoxStore';
import { SlidePanel } from '../ui/SlidePanel/SlidePanel';
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

export default function IssueDetailSlide() {
  const selectedIdentifier = useItervoxStore((s) => s.selectedIdentifier);
  const setSelectedIdentifier = useItervoxStore((s) => s.setSelectedIdentifier);
  const availableProfiles = useItervoxStore((s) => s.snapshot?.availableProfiles ?? EMPTY_PROFILES);
  const queryClient = useQueryClient();

  const { data: issuesList = [] } = useIssues();
  const { data: freshIssue } = useIssue(selectedIdentifier ?? '');
  const issue = freshIssue ?? issuesList.find((i) => i.identifier === selectedIdentifier) ?? null;

  const cancelIssueMutation = useCancelIssue();
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
  const [replyText, setReplyText] = useState('');

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

  const isInReview = issue.state.toLowerCase() === 'in review';
  const isProfileLocked = issue.state.toLowerCase().includes('progress');

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
      />

      {/* Scrollable body */}
      <div className="flex-1 space-y-5 overflow-y-auto px-5 py-4">
        <div className="flex items-center gap-3">
          {issue.url && (
            <a
              href={issue.url}
              target="_blank"
              rel="noopener noreferrer"
              className="text-theme-accent text-sm hover:underline"
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
              <span className="bg-theme-warning-soft text-theme-warning inline-flex items-center rounded px-2 py-0.5 text-xs font-medium">
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
                <span className="bg-theme-warning-soft text-theme-warning rounded px-1.5 py-0.5 text-[10px]">
                  locked while In Progress
                </span>
              </div>
            ) : (
              <select
                value={issue.agentProfile ?? ''}
                onChange={(e) => {
                  setIssueProfileMutation.mutate({
                    identifier: issue.identifier,
                    profile: e.target.value,
                  });
                }}
                className="rounded-[var(--radius-sm)] border px-3 py-2 text-[13px] focus:outline-none"
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

        <IssueBlockerDetails issue={issue} />

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

        {/* Input Required — reply UI */}
        {issue.orchestratorState === 'input_required' && (
          <div className="space-y-3 rounded-lg border border-orange-500/30 bg-orange-500/5 p-4">
            <div className="flex items-center gap-2">
              <span className="h-2.5 w-2.5 rounded-full bg-orange-400" />
              <h4 className="text-sm font-semibold text-orange-400">Agent needs your input</h4>
            </div>
            {issue.error && <MarkdownPanel>{issue.error}</MarkdownPanel>}
            <textarea
              value={replyText}
              onChange={(e) => {
                setReplyText(e.target.value);
              }}
              placeholder="Type your reply… (will be posted as a comment to the tracker)"
              rows={4}
              className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted w-full rounded-lg border px-3 py-2 text-sm focus:ring-1 focus:ring-orange-400 focus:outline-none"
            />
            <div className="flex items-center gap-2">
              <button
                onClick={() => {
                  if (!replyText.trim()) return;
                  provideInputMutation.mutate(
                    { identifier: issue.identifier, message: replyText.trim() },
                    {
                      onSuccess: () => {
                        setReplyText('');
                      },
                    },
                  );
                }}
                disabled={provideInputMutation.isPending || !replyText.trim()}
                className="rounded-lg bg-orange-500 px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
              >
                {provideInputMutation.isPending ? 'Sending…' : 'Reply & Resume Agent'}
              </button>
              <button
                onClick={() => {
                  dismissInputMutation.mutate(issue.identifier);
                }}
                disabled={dismissInputMutation.isPending}
                className="text-theme-text-secondary bg-theme-bg-soft rounded-lg px-4 py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50"
              >
                {dismissInputMutation.isPending ? 'Dismissing…' : 'Dismiss'}
              </button>
            </div>
          </div>
        )}

        {issue.orchestratorState === 'pending_input_resume' && (
          <div className="space-y-3 rounded-lg border border-orange-500/30 bg-orange-500/5 p-4">
            <div className="flex items-center gap-2">
              <span className="h-2.5 w-2.5 rounded-full bg-orange-400" />
              <h4 className="text-sm font-semibold text-orange-400">Reply received</h4>
            </div>
            <p className="text-theme-text-secondary text-sm">
              Itervox has your reply and is waiting to resume the agent.
            </p>
            {issue.error && <MarkdownPanel>{issue.error}</MarkdownPanel>}
          </div>
        )}
      </div>

      {/* Sticky action footer */}
      {(issue.orchestratorState === 'running' ||
        issue.orchestratorState === 'retrying' ||
        issue.orchestratorState === 'paused' ||
        issue.orchestratorState === 'input_required' ||
        isInReview) && (
        <div className="border-theme-line flex flex-shrink-0 items-center justify-between gap-3 border-t px-5 py-4">
          {reviewerProfile && issue.orchestratorState !== 'running' && (
            <button
              onClick={() => {
                triggerAIReviewMutation.mutate(issue.identifier);
              }}
              disabled={triggerAIReviewMutation.isPending}
              className="rounded-[var(--radius-sm)] border px-3 py-1.5 text-xs font-medium transition-colors hover:opacity-80"
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

          <div className="ml-auto flex items-center gap-2">
            {/* Paused state */}
            {issue.orchestratorState === 'paused' && (
              <>
                <button
                  onClick={() => {
                    resumeIssueMutation.mutate(issue.identifier);
                    close();
                  }}
                  disabled={resumeIssueMutation.isPending}
                  className="bg-theme-success rounded-lg px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
                >
                  {resumeIssueMutation.isPending ? 'Resuming…' : '▶ Resume Agent'}
                </button>
                <button
                  onClick={() => {
                    terminateIssueMutation.mutate(issue.identifier);
                  }}
                  disabled={terminateIssueMutation.isPending}
                  className="bg-theme-danger rounded-lg px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
                >
                  {terminateIssueMutation.isPending ? 'Discarding…' : '✕ Discard'}
                </button>
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
                  className="bg-theme-warning rounded-lg px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
                >
                  {cancelIssueMutation.isPending ? 'Pausing…' : '⏸ Pause Agent'}
                </button>
                <button
                  onClick={() => {
                    terminateIssueMutation.mutate(issue.identifier);
                  }}
                  disabled={cancelIssueMutation.isPending || terminateIssueMutation.isPending}
                  className="bg-theme-danger rounded-lg px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
                >
                  {terminateIssueMutation.isPending ? 'Cancelling…' : '✕ Cancel Agent'}
                </button>
              </>
            )}

            {/* Retrying state */}
            {issue.orchestratorState === 'retrying' && (
              <button
                onClick={() => {
                  cancelIssueMutation.mutate(issue.identifier);
                }}
                disabled={cancelIssueMutation.isPending}
                className="bg-theme-warning rounded-lg px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
              >
                {cancelIssueMutation.isPending ? 'Cancelling…' : '✕ Cancel Retry'}
              </button>
            )}
          </div>
        </div>
      )}
    </SlidePanel>
  );
}
