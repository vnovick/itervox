import type { useDismissInput, useProvideInput } from '../../../queries/issues';
import { InputReplyBox } from '../../../components/itervox/InputReplyBox';
import { PipelineChangeNotice } from '../../../components/itervox/PipelineChangeNotice';
import { RequireScope } from '../../../components/itervox/RequireScope';
import { plainButtonClass } from '../../../components/ui/button/buttonRecipe';
import { NEEDS_YOU_KIND_LABEL, isDecision } from '../../../lib/needsYou';
import type { NeedsYouRow } from '../../../types/schemas';

const ACTION_BUTTON = plainButtonClass('secondary', 'md', { pill: true });
const PRIMARY_ACTION_BUTTON = plainButtonClass('primary', 'md', { pill: true });

const KIND_PILL: Record<NeedsYouRow['kind'], string> = {
  question: 'pill pill-accent',
  approval: 'pill pill-warn',
  'pipeline-change': 'pill pill-danger',
};

export interface NeedsYouItemProps {
  row: NeedsYouRow;
  /** How long it has waited ("2h 14m"); undefined when unknown. */
  wait: string | undefined;
  inlineInput: boolean;
  replyOpen: boolean;
  onToggleReply: () => void;
  onOpen: () => void;
  draft: string;
  onDraftChange: (draft: string) => void;
  provideInput: ReturnType<typeof useProvideInput>;
  dismissInput: ReturnType<typeof useDismissInput>;
  sending: boolean;
  dismissing: boolean;
}

// One row of the "Needs you" lane: the wait, the issue, the kind, the one
// line it is stuck on, and the existing answer paths — a question opens the
// shared InputReplyBox; an approval (or a held pipeline push) is decided
// with the same "approve" / "reject" provide-input answer the phone and
// `itervox approve` send. Operator scope; the daemon enforces it.
export function NeedsYouItem({
  row,
  wait,
  inlineInput,
  replyOpen,
  onToggleReply,
  onOpen,
  draft,
  onDraftChange,
  provideInput,
  dismissInput,
  sending,
  dismissing,
}: NeedsYouItemProps) {
  const id = row.identifier;
  const decision = isDecision(row.kind);
  return (
    <li data-testid={`needs-you-item-${id}`} className="glass-card px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <span
          data-testid="needs-you-wait"
          className="text-theme-warning-text w-[4.5rem] shrink-0 font-mono text-xs font-semibold tabular-nums"
          title={row.waitingSince ? `Waiting since ${row.waitingSince}` : undefined}
        >
          {wait ?? '—'}
        </span>
        <span className="text-theme-text font-mono text-xs font-medium">{id}</span>
        <span className={KIND_PILL[row.kind]}>{NEEDS_YOU_KIND_LABEL[row.kind]}</span>
        <div className="min-w-0 flex-1">
          {row.title && <span className="text-theme-text-secondary mr-2 text-xs">{row.title}</span>}
          <span className="text-theme-text text-sm break-words">{row.summary}</span>
        </div>
        <div className="flex flex-shrink-0 flex-wrap items-center gap-1.5">
          <RequireScope scope="operator">
            {decision ? (
              <>
                <button
                  type="button"
                  className={PRIMARY_ACTION_BUTTON}
                  aria-label={`Approve ${id}`}
                  disabled={sending}
                  onClick={() => {
                    provideInput.mutate({ identifier: id, message: 'approve' });
                  }}
                >
                  Approve
                </button>
                <button
                  type="button"
                  className={ACTION_BUTTON}
                  aria-label={`Reject ${id}`}
                  disabled={sending}
                  onClick={() => {
                    provideInput.mutate({ identifier: id, message: 'reject' });
                  }}
                >
                  Reject
                </button>
              </>
            ) : (
              <button
                type="button"
                className={PRIMARY_ACTION_BUTTON}
                aria-label={`Answer ${id}`}
                aria-expanded={replyOpen}
                aria-controls={`needs-you-reply-${id}`}
                onClick={onToggleReply}
              >
                Answer
              </button>
            )}
          </RequireScope>
          <button
            type="button"
            className={ACTION_BUTTON}
            aria-label={`Open ${id}`}
            onClick={onOpen}
          >
            Open
          </button>
        </div>
      </div>
      {row.kind === 'pipeline-change' && (
        <div className="mt-2">
          <PipelineChangeNotice identifier={id} />
        </div>
      )}
      {replyOpen && !decision && (
        <div id={`needs-you-reply-${id}`} className="mt-2 space-y-2">
          <RequireScope scope="operator">
            <InputReplyBox
              identifier={id}
              inlineInput={inlineInput}
              draft={draft}
              onDraftChange={onDraftChange}
              provideInputMutation={provideInput}
              dismissInputMutation={dismissInput}
              sending={sending}
              dismissing={dismissing}
              nameWithIdentifier
            />
          </RequireScope>
        </div>
      )}
    </li>
  );
}
