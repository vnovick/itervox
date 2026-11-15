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

// The waiting question is the one warm card on the page. On a phone the
// shared reply box's Send fills the row at a thumb-sized height; the box
// itself (shared with the issue detail) is unchanged.
const REPLY_LAYOUT =
  '[&_[data-testid=input-reply-send]]:h-12 [&_[data-testid=input-reply-send]]:flex-1 [&_[data-testid=input-reply-send]]:font-semibold';

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
        data-testid="mobile-decision"
        className={`glass-card border-theme-warning/30 flex flex-col gap-3 rounded-[var(--radius-lg)] bg-[linear-gradient(var(--warning-soft),var(--warning-soft)),var(--card)] p-4 ${REPLY_LAYOUT}`}
      >
        <h2 className="pill pill-warn self-start">
          {run.pendingApproval ? 'Waiting for your approval' : 'Waiting for your answer'}
        </h2>
        <p className="text-theme-text text-lg leading-snug font-semibold break-words">
          {run.pendingInput}
        </p>
        {run.pendingApproval ? (
          <div className="grid grid-cols-2 gap-2">
            <Button
              variant="primary"
              size="lg"
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
