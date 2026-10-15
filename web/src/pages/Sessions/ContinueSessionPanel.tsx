import { useState } from 'react';
import { Button } from '../../components/ui/button';
import { useContinueSession } from '../../queries/sessions';
import type { SessionRow } from '../../types/schemas';

/**
 * A session started outside itervox is read-only. "Continue in itervox"
 * resumes it under itervox — only once the CLI that wrote it has exited;
 * while it may still run, the action is refused and the reason shown.
 * After that, the session is itervox's and its composer works.
 */
export function ContinueSessionPanel({ session }: { session: SessionRow }) {
  const [draft, setDraft] = useState('');
  const cont = useContinueSession();
  const inUse = session.status === 'running';

  return (
    <div className="space-y-2" data-testid="session-readonly">
      <p className="text-theme-text-secondary text-sm">
        Started outside itervox, so it is read-only here.
      </p>
      {inUse ? (
        <p data-testid="session-in-use" className="text-sm text-orange-400">
          Still running outside itervox{session.inUseReason ? `: ${session.inUseReason}` : ''}.
          Continue it once that process has exited.
        </p>
      ) : (
        <textarea
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
          }}
          rows={2}
          aria-label="First message when continuing (optional)"
          placeholder="What should the agent do next? (optional)"
          className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted w-full rounded-lg border px-3 py-2 text-sm focus:ring-1 focus:ring-orange-400 focus:outline-none"
        />
      )}
      <div className="flex flex-wrap items-center gap-3">
        <Button
          variant="primary"
          size="sm"
          disabled={inUse || cont.isPending}
          aria-describedby={inUse ? 'session-in-use-note' : undefined}
          onClick={() => {
            cont.mutate(
              { id: session.id, message: draft.trim() },
              {
                onSuccess: () => {
                  setDraft('');
                },
              },
            );
          }}
        >
          {cont.isPending ? 'Continuing…' : 'Continue in itervox'}
        </Button>
        <span id="session-in-use-note" className="text-theme-muted text-xs">
          {inUse
            ? 'Refused while the original process may still be running.'
            : `Resumes the session in ${session.cwd ?? 'its directory'} (not an itervox worktree).`}
        </span>
      </div>
    </div>
  );
}
