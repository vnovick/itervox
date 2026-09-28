import { useState } from 'react';
import MarkdownPanel from './MarkdownPanel';
import { InputReplyBox } from './InputReplyBox';
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
// since it is only ever used within this panel. The reply box itself is
// InputReplyBox, shared with the attention inbox (CORE-077).
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
      <InputReplyBox
        identifier={issue.identifier}
        inlineInput={inlineInput}
        draft={replyText}
        onDraftChange={setReplyText}
        provideInputMutation={provideInputMutation}
        dismissInputMutation={dismissInputMutation}
        sending={provideInputMutation.isPending}
        dismissing={dismissInputMutation.isPending}
        onSent={() => {
          setReplyText('');
        }}
      />
    </div>
  );
}
