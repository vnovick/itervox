import { useState } from 'react';
import { Button } from '../../components/ui/button';
import { ConfirmModal } from '../../components/ui/modal/ConfirmModal';
import { useSendSessionMessage } from '../../queries/sessions';
import {
  useKillSession,
  useSendSessionKey,
  useSessionScreen,
  type SessionKey,
} from '../../queries/sessionsTmux';
import type { SessionRow } from '../../types/schemas';
import { CopyButton } from './TmuxNotice';

// #178 tmux control: the live screen of a session running in a tmux pane,
// a composer that pastes text (and presses Enter), a row of keys, and — for
// a session itervox started — its attach command and Stop.

const KEYS: { key: SessionKey; label: string; title: string }[] = [
  { key: 'Escape', label: 'Esc', title: 'Escape' },
  { key: 'Enter', label: 'Enter', title: 'Enter' },
  { key: 'Up', label: '↑', title: 'Up' },
  { key: 'Down', label: '↓', title: 'Down' },
  { key: '1', label: '1', title: 'Answer 1' },
  { key: '2', label: '2', title: 'Answer 2' },
  { key: '3', label: '3', title: 'Answer 3' },
];

/** The pane's text, polled while the tab is visible. Plain text only. */
export function TmuxScreen({ id }: { id: string }) {
  const { data, isError, error } = useSessionScreen(id, true);
  if (isError) {
    return (
      <p data-testid="tmux-screen-error" className="text-theme-danger-text text-sm">
        {error instanceof Error ? error.message : 'The screen could not be read.'}
      </p>
    );
  }
  return (
    <pre
      data-testid="tmux-screen"
      aria-label="Session screen"
      className="bg-theme-bg-elevated text-theme-text min-h-[240px] flex-1 overflow-auto rounded-lg p-3 font-mono text-xs leading-snug whitespace-pre"
    >
      {data?.text ?? 'Reading the screen…'}
    </pre>
  );
}

/** Composer, keys, attach command and Stop of a controllable session. */
export function TmuxControls({ session }: { session: SessionRow }) {
  const [draft, setDraft] = useState('');
  const [confirm, setConfirm] = useState<'interrupt' | 'stop' | null>(null);
  const send = useSendSessionMessage();
  const sendKey = useSendSessionKey();
  const kill = useKillSession();
  const owned = session.kind === 'tmux';
  const submit = (withEnter: boolean) => {
    const text = draft.trim();
    if (!text) return;
    send.mutate(
      { id: session.id, message: text, submit: withEnter },
      {
        onSuccess: () => {
          setDraft('');
        },
      },
    );
  };
  return (
    <div className="space-y-2" data-testid="tmux-controls">
      <textarea
        value={draft}
        onChange={(e) => {
          setDraft(e.target.value);
        }}
        rows={2}
        aria-label="Text to type into the session"
        placeholder="Type into the session…"
        className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted w-full rounded-lg border px-3 py-2 text-sm focus:ring-1 focus:ring-orange-400 focus:outline-none"
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="primary"
          size="sm"
          disabled={send.isPending || !draft.trim()}
          onClick={() => {
            submit(true);
          }}
        >
          Send
        </Button>
        <Button
          size="sm"
          disabled={send.isPending || !draft.trim()}
          onClick={() => {
            submit(false);
          }}
        >
          Type without Enter
        </Button>
        <span className="text-theme-muted mx-1 text-xs">Keys:</span>
        {KEYS.map((k) => (
          <Button
            key={k.key}
            size="sm"
            variant="ghost"
            title={k.title}
            aria-label={`Press ${k.title}`}
            disabled={sendKey.isPending}
            onClick={() => {
              sendKey.mutate({ id: session.id, key: k.key });
            }}
          >
            {k.label}
          </Button>
        ))}
        <Button
          size="sm"
          variant="danger"
          aria-label="Press Ctrl-C"
          onClick={() => {
            setConfirm('interrupt');
          }}
        >
          Ctrl-C
        </Button>
      </div>
      {owned && session.attachCommand && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="text-theme-muted">Attach from a terminal:</span>
          <code data-testid="tmux-attach" className="font-mono">
            {session.attachCommand}
          </code>
          <CopyButton text={session.attachCommand} label="Copy the attach command" />
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setConfirm('stop');
            }}
          >
            Stop session
          </Button>
        </div>
      )}
      <ConfirmModal
        isOpen={confirm !== null}
        onClose={() => {
          setConfirm(null);
        }}
        onConfirm={() => {
          if (confirm === 'interrupt') sendKey.mutate({ id: session.id, key: 'C-c' });
          else kill.mutate(session.id);
          setConfirm(null);
        }}
        title={confirm === 'stop' ? 'Stop this session?' : 'Send Ctrl-C?'}
        description={
          confirm === 'stop'
            ? 'Kills its tmux session and the agent in it.'
            : 'Interrupts what the agent is doing.'
        }
        confirmLabel={confirm === 'stop' ? 'Stop session' : 'Send Ctrl-C'}
      />
    </div>
  );
}
