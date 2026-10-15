import { useState } from 'react';
import { InputReplyBox } from '../../components/itervox/InputReplyBox';
import { Button } from '../../components/ui/button';
import { useDismissInput, useProvideInput } from '../../queries/issues';
import { sessionKey, useSendSessionMessage } from '../../queries/sessions';
import type { SessionRow } from '../../types/schemas';
import { SessionMessages } from './SessionMessages';

export interface SessionComposerProps {
  session: SessionRow;
  inlineInput: boolean;
}

/**
 * The composer of one session tab. Itervox writes only to sessions it
 * owns: anything else is read-only. A session waiting for input takes a
 * reply through the existing provide-input path; a running one takes an
 * operator message (live into the turn, or on the next resumed turn).
 */
export function SessionComposer({ session, inlineInput }: SessionComposerProps) {
  const [draft, setDraft] = useState('');
  const provideInput = useProvideInput();
  const dismissInput = useDismissInput();
  const send = useSendSessionMessage();

  if (session.owner !== 'itervox') {
    return (
      <div className="space-y-1" data-testid="session-readonly">
        <p className="text-theme-text-secondary text-sm">
          Started outside itervox — read-only.{' '}
          {session.status === 'running'
            ? 'Its CLI is still running.'
            : 'Its CLI is no longer running.'}
        </p>
        {session.status === 'running' && session.inUseReason && (
          <p data-testid="session-running-reason" className="text-theme-muted text-xs">
            {session.inUseReason}
          </p>
        )}
      </div>
    );
  }

  if (session.status === 'input_required') {
    return (
      <div className="space-y-2" data-testid="session-reply">
        {session.inputContext && (
          <p className="text-theme-text text-sm whitespace-pre-wrap">{session.inputContext}</p>
        )}
        <InputReplyBox
          identifier={session.identifier}
          inlineInput={inlineInput}
          draft={draft}
          onDraftChange={setDraft}
          provideInputMutation={provideInput}
          dismissInputMutation={dismissInput}
          sending={provideInput.isPending}
          dismissing={dismissInput.isPending}
          onSent={() => {
            setDraft('');
          }}
        />
      </div>
    );
  }

  if (session.status !== 'running') {
    return (
      <p className="text-theme-text-secondary text-sm">Reply received — the agent is resuming.</p>
    );
  }

  const live = session.steering === 'live';
  const label = session.identifier || 'this session';
  return (
    <div className="space-y-2" data-testid="session-composer">
      <SessionMessages messages={session.messages} />
      <textarea
        value={draft}
        onChange={(e) => {
          setDraft(e.target.value);
        }}
        rows={3}
        aria-label={`Message to the agent on ${label}`}
        placeholder="Message the running agent…"
        className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted w-full rounded-lg border px-3 py-2 text-sm focus:ring-1 focus:ring-orange-400 focus:outline-none"
      />
      <div className="flex items-center gap-3">
        <Button
          variant="primary"
          size="sm"
          disabled={send.isPending || !draft.trim()}
          onClick={() => {
            const message = draft.trim();
            if (!message) return;
            send.mutate(
              { id: sessionKey(session), message },
              {
                onSuccess: () => {
                  setDraft('');
                },
              },
            );
          }}
        >
          {send.isPending ? 'Sending…' : 'Send'}
        </Button>
        <span data-testid="session-steering" className="text-theme-muted text-xs">
          {live
            ? 'Delivered into the running turn.'
            : 'Delivered when the current turn ends, by resuming the session.'}
        </span>
      </div>
    </div>
  );
}
