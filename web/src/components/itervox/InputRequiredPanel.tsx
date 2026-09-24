import { useState } from 'react';
import MarkdownPanel from './MarkdownPanel';
import type { useDismissInput, useProvideInput } from '../../queries/issues';
import type { TrackerIssue } from '../../types/schemas';

interface InputRequiredPanelProps {
  issue: Pick<TrackerIssue, 'identifier' | 'orchestratorState' | 'error'>;
  inlineInput: boolean;
  provideInputMutation: ReturnType<typeof useProvideInput>;
  dismissInputMutation: ReturnType<typeof useDismissInput>;
}

// Extracted from IssueDetailSlide (Task 5, inline-input outbox sub-project) to
// keep the parent under the size-budget cap. Owns the reply-box local state
// since it is only ever used within this panel.
export function InputRequiredPanel({
  issue,
  inlineInput,
  provideInputMutation,
  dismissInputMutation,
}: InputRequiredPanelProps) {
  const [replyText, setReplyText] = useState('');

  if (issue.orchestratorState === 'pending_input_resume') {
    return (
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
    );
  }

  if (issue.orchestratorState !== 'input_required') return null;

  return (
    <div className="space-y-3 rounded-lg border border-orange-500/30 bg-orange-500/5 p-4">
      <div className="flex items-center gap-2">
        <span className="h-2.5 w-2.5 rounded-full bg-orange-400" />
        <h4 className="text-sm font-semibold text-orange-400">Agent needs your input</h4>
      </div>
      {issue.error && <MarkdownPanel>{issue.error}</MarkdownPanel>}
      {inlineInput ? (
        <p data-testid="input-required-inline-notice" className="text-theme-text-secondary text-sm">
          Reply by commenting on this issue in your tracker — the agent resumes from your comment.
          Dashboard replies are turned off (Settings → General → Inline input).
        </p>
      ) : (
        <textarea
          value={replyText}
          onChange={(e) => {
            setReplyText(e.target.value);
          }}
          placeholder="Type your reply… (will be posted as a comment to the tracker)"
          aria-label="Reply to the agent"
          rows={4}
          className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted w-full rounded-lg border px-3 py-2 text-sm focus:ring-1 focus:ring-orange-400 focus:outline-none"
        />
      )}
      <div className="flex items-center gap-2">
        {!inlineInput && (
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
        )}
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
  );
}
