import { useRef } from 'react';
import type { useDismissInput, useProvideInput } from '../../queries/issues';

export interface InputReplyBoxProps {
  identifier: string;
  inlineInput: boolean;
  draft: string;
  onDraftChange: (draft: string) => void;
  provideInputMutation: ReturnType<typeof useProvideInput>;
  dismissInputMutation: ReturnType<typeof useDismissInput>;
  /** A reply for this issue is in flight (the caller knows which ids are). */
  sending: boolean;
  /** A dismiss for this issue is in flight. */
  dismissing: boolean;
  /**
   * Per-call success hook for single-box callers (the issue panel). Lists
   * clear drafts from useProvideInput's hook-level `onSent` instead, which
   * fires for every call, not only the latest (M5-close BH-M5-3).
   */
  onSent?: () => void;
  /**
   * Put the issue identifier into every accessible name. Set where several
   * reply boxes can share a page (the attention inbox, CORE-077).
   */
  nameWithIdentifier?: boolean;
}

// The input-required reply box, shared by the issue detail slide
// (InputRequiredPanel) and the attention inbox (CORE-077). Controlled: the
// caller owns the draft, so the inbox can keep one draft per issue.
//
// With `inlineInput` on, the daemon answers provide-input with 409
// inline_input_enabled, so the textarea is replaced by the tracker notice
// (Dismiss stays). useProvideInput's onError refreshes the snapshot on that
// 409, which flips `inlineInput` here without a reload.
export function InputReplyBox({
  identifier,
  inlineInput,
  draft,
  onDraftChange,
  provideInputMutation,
  dismissInputMutation,
  nameWithIdentifier = false,
  sending,
  dismissing,
  onSent,
}: InputReplyBoxProps) {
  // M5-close — Send disables while in flight and again once the draft clears,
  // which dropped focus to <body>; keep it in the textarea instead.
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const suffix = nameWithIdentifier ? ` on ${identifier}` : '';

  return (
    <>
      {inlineInput ? (
        <p data-testid="input-required-inline-notice" className="text-theme-text-secondary text-sm">
          Reply by commenting on this issue in your tracker — the agent resumes from your comment.
          Dashboard replies are turned off (Settings → General → Inline input).
        </p>
      ) : (
        <textarea
          ref={textareaRef}
          value={draft}
          onChange={(e) => {
            onDraftChange(e.target.value);
          }}
          placeholder="Type your reply… (will be posted as a comment to the tracker)"
          aria-label={`Reply to the agent${suffix}`}
          rows={4}
          className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted w-full rounded-lg border px-3 py-2 text-sm focus:ring-1 focus:ring-orange-400 focus:outline-none"
        />
      )}
      <div className="flex items-center gap-2">
        {!inlineInput && (
          <button
            type="button"
            data-testid="input-reply-send"
            aria-label={nameWithIdentifier ? `Send reply to ${identifier}` : undefined}
            onClick={() => {
              const message = draft.trim();
              if (!message) return;
              provideInputMutation.mutate(
                { identifier, message },
                onSent ? { onSuccess: onSent } : undefined,
              );
              textareaRef.current?.focus();
            }}
            disabled={sending || !draft.trim()}
            className="bg-theme-accent rounded-lg px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
          >
            {sending ? 'Sending…' : 'Reply & Resume Agent'}
          </button>
        )}
        <button
          type="button"
          aria-label={nameWithIdentifier ? `Dismiss input request for ${identifier}` : undefined}
          onClick={() => {
            dismissInputMutation.mutate(identifier);
          }}
          disabled={dismissing}
          className="text-theme-text-secondary bg-theme-bg-soft rounded-lg px-4 py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50"
        >
          {dismissing ? 'Dismissing…' : 'Dismiss'}
        </button>
      </div>
    </>
  );
}
