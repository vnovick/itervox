import { useState } from 'react';
import { useRunContext } from '../../queries/changes';
import { useSendSessionMessage } from '../../queries/sessions';
import { RequireScope } from './RequireScope';
import { SteeringChips } from './SteeringChips';

/**
 * #218 — the issue detail's steer box for a running issue: a message, or a
 * one-click steering chip, sent to the run through the Sessions steering
 * endpoint (POST /api/v1/sessions/{runId}/message). The run id comes from
 * the run context (#215). Operator scope.
 */
export function IssueSteerBox({ identifier }: { identifier: string }) {
  const [draft, setDraft] = useState('');
  const { data } = useRunContext(identifier, { enabled: true, live: true });
  const send = useSendSessionMessage();
  const runId = data?.run?.live ? data.run.runId : '';
  if (!runId) return null;

  const submit = (message: string) => {
    const text = message.trim();
    if (!text) return;
    send.mutate(
      { id: runId, message: text },
      {
        onSuccess: () => {
          setDraft('');
        },
      },
    );
  };

  return (
    <RequireScope scope="operator">
      <section data-testid="issue-steer-box" className="space-y-2">
        <h4 className="text-xs font-medium tracking-wider uppercase">Steer the run</h4>
        <SteeringChips onSend={submit} disabled={send.isPending} />
        <textarea
          aria-label={`Message to the agent on ${identifier}`}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
          }}
          rows={2}
          placeholder="Message the running agent…"
          className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted w-full rounded-lg border px-3 py-2 text-sm focus:ring-1 focus:ring-orange-400 focus:outline-none"
        />
        <div className="flex justify-end">
          <button
            type="button"
            disabled={send.isPending || !draft.trim()}
            onClick={() => {
              submit(draft);
            }}
            className="bg-theme-accent rounded-lg px-4 py-1.5 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
          >
            {send.isPending ? 'Sending…' : 'Send'}
          </button>
        </div>
      </section>
    </RequireScope>
  );
}
