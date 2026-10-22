import { useState } from 'react';
import { Button } from '../../ui/button';
import { InputReplyBox } from '../InputReplyBox';
import { RequireScope } from '../RequireScope';
import { useDismissInput, useProvideInput } from '../../../queries/issues';
import { useRunContext } from '../../../queries/changes';
import { useItervoxStore } from '../../../store/itervoxStore';

// #231 — what the phone can decide for a waiting issue: a guardrail
// approval gets Approve and Reject (the same "approve" / "reject" answer
// the dashboard and Slack send through provide-input); a question gets the
// shared reply box. Operator scope; the daemon enforces it.

export function MobileIssueDecision({ identifier }: { identifier: string }) {
  const { data } = useRunContext(identifier, { enabled: true, live: true });
  const inlineInput = useItervoxStore((s) => s.snapshot?.inlineInput ?? false);
  const [draft, setDraft] = useState('');
  const provide = useProvideInput({
    onSent: () => {
      setDraft('');
    },
  });
  const dismiss = useDismissInput();
  const run = data?.run;
  if (!run?.pendingInput) return null;

  return (
    <RequireScope scope="operator">
      <section
        aria-label={run.pendingApproval ? 'Approval' : 'Reply'}
        className="border-theme-warning-text/30 flex flex-col gap-2 rounded-[var(--radius-sm)] border p-3"
      >
        <h2 className="text-theme-text text-sm font-semibold">
          {run.pendingApproval ? 'Waiting for your approval' : 'Waiting for your answer'}
        </h2>
        <p className="text-theme-text-secondary text-sm break-words">{run.pendingInput}</p>
        {run.pendingApproval ? (
          <div className="grid grid-cols-2 gap-2">
            <Button
              variant="primary"
              className="h-12"
              disabled={provide.isPending}
              onClick={() => {
                provide.mutate({ identifier, message: 'approve' });
              }}
            >
              Approve
            </Button>
            <Button
              variant="danger"
              className="h-12"
              disabled={provide.isPending}
              onClick={() => {
                provide.mutate({ identifier, message: 'reject' });
              }}
            >
              Reject
            </Button>
          </div>
        ) : (
          <InputReplyBox
            identifier={identifier}
            inlineInput={inlineInput}
            draft={draft}
            onDraftChange={setDraft}
            provideInputMutation={provide}
            dismissInputMutation={dismiss}
            sending={provide.isPending}
            dismissing={dismiss.isPending}
          />
        )}
      </section>
    </RequireScope>
  );
}
