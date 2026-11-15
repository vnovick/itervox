// One eval case in Repo Analysis → Evals (#201): its latest pass/fail, the
// assertions that failed, the prompt digest it ran against, when, and the
// regression marker when it passed on the previous digest.

import type { RepoEvalCase } from '../../../types/schemas';
import { shortDigest } from '../../../utils/promptVersions';
import { fmtSeen } from '../prompts/promptVersionGroups';

const BADGE = 'pill pill-sm';
const PASS_STYLE = 'pill-ok';
const FAIL_STYLE = 'pill-danger';

const DIGEST_SOURCE_LABEL: Record<string, string> = {
  live: 'computed by the live run',
  recording: 'from the recording',
};

export function EvalCaseRow({ c }: { c: RepoEvalCase }) {
  const failures = c.failures ?? [];
  return (
    <li className="glass-card px-3.5 py-3" data-case={`${c.profile}/${c.scenario}`}>
      <div className="flex flex-wrap items-center gap-2">
        <span className={`${BADGE} ${c.pass ? PASS_STYLE : FAIL_STYLE}`}>
          {c.pass ? 'pass' : 'fail'}
        </span>
        {c.regression && <span className={`${BADGE} pill-warn`}>regression</span>}
        <span className="text-theme-text font-mono text-xs font-semibold">{c.scenario}</span>
        {c.stale && <span className="text-theme-muted text-[10px]">(stale recording)</span>}
        <span className="text-theme-muted ml-auto text-[11px]">
          {c.mode} · {fmtSeen(c.ranAt)}
        </span>
      </div>
      <p className="text-theme-muted mt-1 text-[11px]">
        Prompt version:{' '}
        {c.promptDigest ? (
          <>
            <code className="text-theme-text font-mono" title={c.promptDigest}>
              {shortDigest(c.promptDigest)}
            </code>
            {c.digestSource && ` (${DIGEST_SOURCE_LABEL[c.digestSource] ?? c.digestSource})`}
          </>
        ) : (
          'unknown (the recording carries no digest)'
        )}
      </p>
      {c.regression && c.previousDigest && (
        <p className="text-theme-warning-text mt-1 text-[11px]">
          Passed on <code className="font-mono">{shortDigest(c.previousDigest)}</code>
          {c.previousAt && ` (${fmtSeen(c.previousAt)})`}, fails on{' '}
          <code className="font-mono">{shortDigest(c.promptDigest ?? '')}</code>.
        </p>
      )}
      {c.error && <p className="text-theme-danger-text mt-1 text-xs">Run: {c.error}</p>}
      {failures.length > 0 && (
        <ul
          className="mt-1 list-disc pl-5 text-xs"
          aria-label={`Failed assertions of ${c.scenario}`}
        >
          {failures.map((f) => (
            <li key={f} className="text-theme-text">
              {f}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}
