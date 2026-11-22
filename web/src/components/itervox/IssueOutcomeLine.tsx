// Cost and outcome visibility: one line in the issue detail summing the
// issue's recorded runs (runs, retries, turns, wall time, tokens) and what
// became of its latest PR. Data: TrackerIssue.outcome (single-issue read).

import type { IssueOutcome } from '../../types/schemas';
import { fmtMs, fmtTokens } from '../../utils/format';

const PR_TONES: Record<string, string> = {
  merged: 'bg-theme-success-soft text-theme-success-text',
  reverted: 'bg-theme-danger-soft text-theme-danger-text',
  closed: 'bg-theme-warning-soft text-theme-warning-text',
};

const PR_LABELS: Record<string, string> = {
  open: 'PR open',
  merged: 'PR merged',
  closed: 'PR closed unmerged',
  reverted: 'PR reverted',
};

function plural(n: number, word: string): string {
  return `${String(n)} ${word}${n === 1 ? '' : 's'}`;
}

export function IssueOutcomeLine({ outcome }: { outcome?: IssueOutcome }) {
  if (!outcome || outcome.runs === 0) return null;
  const parts = [
    `${plural(outcome.runs, 'run')} (${String(outcome.succeeded)} succeeded)`,
    outcome.retries > 0
      ? `${String(outcome.retries)} ${outcome.retries === 1 ? 'retry' : 'retries'}`
      : '',
    plural(outcome.turns, 'turn'),
    fmtMs(outcome.elapsedMs),
    `${fmtTokens(outcome.inputTokens)} in / ${fmtTokens(outcome.outputTokens)} out tokens`,
    outcome.cacheReadTokens ? `${fmtTokens(outcome.cacheReadTokens)} cache read` : '',
  ].filter(Boolean);
  const pr = outcome.prOutcome;
  return (
    <p
      className="text-theme-text-secondary flex flex-wrap items-center gap-x-2 gap-y-1 text-xs"
      aria-label="Run outcome"
    >
      <span>{parts.join(' · ')}</span>
      <span className="text-theme-muted">last: {outcome.lastStatus.replace('_', ' ')}</span>
      {pr && outcome.prUrl && (
        <a
          href={outcome.prUrl}
          target="_blank"
          rel="noreferrer"
          className={`rounded px-1.5 py-0.5 ${PR_TONES[pr] ?? 'bg-theme-bg-soft text-theme-text-secondary'}`}
        >
          {PR_LABELS[pr] ?? `PR ${pr}`}
        </a>
      )}
    </p>
  );
}
