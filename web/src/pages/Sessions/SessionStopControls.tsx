import { useState } from 'react';
import { Button } from '../../components/ui/button';
import { CopyButton } from '../../components/ui/CopyButton';
import { ConfirmModal } from '../../components/ui/modal/ConfirmModal';
import { RequireScope } from '../../components/itervox/RequireScope';
import { useStopSession } from '../../queries/sessionsTmux';
import type { SessionRow } from '../../types/schemas';

// #178 stopping sessions. A tmux session itervox started (or re-adopted
// after a daemon restart) gets its attach command, Stop — the CLI's
// Ctrl-C, then tmux kill-session if it is still there after a few seconds —
// and Force stop (kill-session at once). A hand-started session in a
// controllable tmux pane gets Stop as keys only: itervox never kills what
// it did not start. Stopping needs an admin token (the daemon enforces it).

const ADOPTED_NOTE = 'Started by itervox (re-adopted after restart).';
const STALE_GUARDRAILS_NOTE =
  'Guardrails from the previous daemon run: they still refuse force pushes, tags and merges, but pushes that guardrails.lanes must report are refused and refusals are not reported here. Restart the session for fresh guardrails.';

type Pending = 'stop' | 'force' | null;

interface DialogText {
  title: string;
  description: string;
  label: string;
}

const DIALOG: Record<'ownedStop' | 'handStop' | 'force', DialogText> = {
  ownedStop: {
    title: 'Stop this session?',
    description:
      'Sends Ctrl-C to the agent; if it is still running after a few seconds, kills its tmux session.',
    label: 'Stop session',
  },
  handStop: {
    title: 'Stop this session?',
    description:
      'Sends Ctrl-C to the agent in its tmux pane. Itervox did not start it, so it never kills it.',
    label: 'Stop session',
  },
  force: {
    title: 'Force stop this session?',
    description: 'Kills its tmux session and the agent in it at once.',
    label: 'Force stop',
  },
};

/** Attach command, Stop and Force stop of a controllable session. */
export function SessionStopControls({ session }: { session: SessionRow }) {
  const [pending, setPending] = useState<Pending>(null);
  const stop = useStopSession();
  const owned = session.owner === 'itervox' && session.kind === 'tmux';
  const dialog = DIALOG[pending === 'force' ? 'force' : owned ? 'ownedStop' : 'handStop'];
  return (
    <div className="space-y-1.5" data-testid="session-stop-controls">
      {owned && session.adopted && (
        <p data-testid="session-adopted" className="text-theme-text-secondary text-xs">
          {ADOPTED_NOTE}
        </p>
      )}
      {owned && session.guardrailsStale && (
        <p data-testid="session-guardrails-stale" className="text-theme-warning-text text-xs">
          {STALE_GUARDRAILS_NOTE}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2 text-xs">
        {owned && session.attachCommand && (
          <>
            <span className="text-theme-muted">Attach from a terminal:</span>
            <code data-testid="tmux-attach" className="font-mono">
              {session.attachCommand}
            </code>
            <CopyButton text={session.attachCommand} label="Copy the attach command" />
          </>
        )}
        <RequireScope scope="admin">
          <Button
            size="sm"
            variant="glass-danger"
            pill
            disabled={stop.isPending}
            onClick={() => {
              setPending('stop');
            }}
          >
            Stop session
          </Button>
          {owned && (
            <Button
              size="sm"
              variant="glass-danger"
              pill
              disabled={stop.isPending}
              onClick={() => {
                setPending('force');
              }}
            >
              Force stop
            </Button>
          )}
        </RequireScope>
      </div>
      <ConfirmModal
        isOpen={pending !== null}
        onClose={() => {
          setPending(null);
        }}
        onConfirm={() => {
          if (pending) stop.mutate({ id: session.id, force: pending === 'force' });
          setPending(null);
        }}
        title={dialog.title}
        description={dialog.description}
        confirmLabel={dialog.label}
      />
    </div>
  );
}

/**
 * The hint under a running hand-started session itervox cannot stop: it
 * runs outside a controllable tmux pane, so it is stopped in its own
 * terminal.
 */
export function ExternalStopHint({ session }: { session: SessionRow }) {
  if (session.owner !== 'external' || session.status !== 'running') return null;
  const kill = session.pid ? `, or kill ${String(session.pid)}` : '';
  return (
    <p data-testid="session-stop-hint" className="text-theme-muted text-xs">
      Stop it in its own terminal: Ctrl-C{kill}.
    </p>
  );
}
