import { useState } from 'react';
import { Button } from '../../components/ui/button';
import { FIELD_CLASS } from '../../components/ui/Input';
import { ConfirmModal } from '../../components/ui/modal/ConfirmModal';
import { useSendSessionMessage } from '../../queries/sessions';
import { useSendSessionKey, useSessionScreen, type SessionKey } from '../../queries/sessionsTmux';
import type { SessionRow } from '../../types/schemas';
import { SessionStopControls } from './SessionStopControls';
import { SCREEN_TONE_CLASS, screenLineTone } from './screenLineTone';

// #178 tmux control: the live screen of a session running in a tmux pane,
// a composer that pastes text (and presses Enter), a row of keys, and
// Stop (SessionStopControls: attach command and Force stop for a session
// itervox started, keys-only Stop for a hand-started one).

const KEYS: { key: SessionKey; label: string; title: string }[] = [
  { key: 'Escape', label: 'Esc', title: 'Escape' },
  { key: 'Enter', label: 'Enter', title: 'Enter' },
  { key: 'Up', label: '↑', title: 'Up' },
  { key: 'Down', label: '↓', title: 'Down' },
  { key: '1', label: '1', title: 'Answer 1' },
  { key: '2', label: '2', title: 'Answer 2' },
  { key: '3', label: '3', title: 'Answer 3' },
];

/** The pane's lines, each tinted when its leading glyph says what it is. */
function ScreenLines({ text }: { text: string }) {
  const lines = text.split('\n');
  return lines.map((line, i) => {
    const tone = screenLineTone(line);
    const end = i < lines.length - 1 ? '\n' : '';
    // Lines are positional (a redraw replaces the whole pane), so the
    // index is the identity here.
    return (
      <span
        key={i}
        data-tone={tone ?? undefined}
        className={tone ? SCREEN_TONE_CLASS[tone] : undefined}
      >
        {line}
        {end}
      </span>
    );
  });
}

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
      className="text-theme-text min-h-[240px] flex-1 overflow-auto px-4 py-3.5 font-mono text-[12.5px] leading-relaxed whitespace-pre"
    >
      {data ? <ScreenLines text={data.text} /> : 'Reading the screen…'}
    </pre>
  );
}

/** Composer, keys, attach command and Stop of a controllable session. */
export function TmuxControls({ session }: { session: SessionRow }) {
  const [draft, setDraft] = useState('');
  const [confirmInterrupt, setConfirmInterrupt] = useState(false);
  const send = useSendSessionMessage();
  const sendKey = useSendSessionKey();
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
    <div className="space-y-3" data-testid="tmux-controls">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-theme-muted mr-1 text-xs">Keys:</span>
        {KEYS.map((k) => (
          <Button
            key={k.key}
            size="lg"
            variant="glass"
            pill
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
          size="lg"
          variant="glass-danger"
          pill
          aria-label="Press Ctrl-C"
          onClick={() => {
            setConfirmInterrupt(true);
          }}
        >
          Ctrl-C
        </Button>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <textarea
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
          }}
          rows={1}
          aria-label="Text to type into the session"
          placeholder="Type into the session…"
          className={`${FIELD_CLASS} min-h-11 min-w-0 flex-1 basis-64 resize-y py-2.5`}
        />
        <Button
          size="lg"
          variant="glass"
          className="h-11"
          disabled={send.isPending || !draft.trim()}
          onClick={() => {
            submit(false);
          }}
        >
          Type without Enter
        </Button>
        <Button
          variant="primary"
          size="lg"
          className="h-11"
          disabled={send.isPending || !draft.trim()}
          onClick={() => {
            submit(true);
          }}
        >
          Send
        </Button>
      </div>
      <SessionStopControls session={session} />
      <ConfirmModal
        isOpen={confirmInterrupt}
        onClose={() => {
          setConfirmInterrupt(false);
        }}
        onConfirm={() => {
          sendKey.mutate({ id: session.id, key: 'C-c' });
          setConfirmInterrupt(false);
        }}
        title="Send Ctrl-C?"
        description="Interrupts what the agent is doing."
        confirmLabel="Send Ctrl-C"
      />
    </div>
  );
}
