import { useId } from 'react';
import { useSetIssueBackend } from '../../queries/issues';
import type { TrackerIssue } from '../../types/schemas';
import { BackendSelector } from './selectors/BackendSelector';

// CORE-056 — per-issue backend pin in the issue detail slide. Extracted from
// IssueDetailSlide (size budget), sibling of InputRequiredPanel /
// IssueCommentComposer. The pin goes through POST /api/v1/issues/{id}/backend
// (useSetIssueBackend: optimistic cache write, rollback + server-message
// toast on error). The daemon checks the pin against the dispatch resolver
// (CORE-115) and refuses a pin that would pair a backend with the other
// backend's command; that refusal is also shown inline, next to the
// control, until the next change.

export function IssueBackendControl({
  issue,
  defaultBackend,
}: {
  issue: TrackerIssue;
  defaultBackend: string;
}) {
  const setIssueBackend = useSetIssueBackend();
  const helpId = useId();
  const errorId = useId();
  const isRunning = issue.orchestratorState === 'running';
  const auto = issue.autoSwitch;
  const err: unknown = setIssueBackend.isError ? setIssueBackend.error : null;
  const refusal: string | null =
    err === null ? null : err instanceof Error ? err.message : 'Backend change refused';
  const describedBy = [helpId, refusal !== null ? errorId : ''].filter(Boolean).join(' ');

  return (
    <div>
      <h4 className="mb-1 text-xs font-medium tracking-wider uppercase">Agent Backend</h4>
      <div className="flex flex-wrap items-center gap-2">
        <BackendSelector
          value={issue.agentBackend ?? ''}
          showLabel={false}
          ariaLabel="Agent backend"
          ariaDescribedBy={describedBy}
          defaultOptionLabel={`Default (${defaultBackend})`}
          disabled={setIssueBackend.isPending}
          onChange={(backend) => {
            setIssueBackend.mutate({ identifier: issue.identifier, backend });
          }}
        />
        {isRunning && (
          <span className="bg-theme-bg-soft text-theme-text-secondary rounded px-1.5 py-0.5 text-[10px]">
            Applies on next run
          </span>
        )}
      </div>
      <p id={helpId} className="text-theme-muted mt-1 text-[11px]">
        {auto
          ? `Auto-switched from ${auto.fromBackend ?? 'unknown'} (${auto.source}). Pick a backend to pin it.`
          : 'Pin this issue to a backend, or use the default.'}
      </p>
      {refusal !== null && (
        <p id={errorId} role="alert" className="text-theme-danger-text mt-1 text-[11px]">
          {refusal}
        </p>
      )}
    </div>
  );
}
