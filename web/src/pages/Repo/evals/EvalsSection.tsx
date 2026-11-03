// Repo Analysis → Evals (#201): the latest persisted eval result per
// profile and scenario, from GET /api/v1/repo/evals. Reports are written by
// `make evals`, `make evals-live`, `make evals-record` and itervox-evals;
// this section only reads them — it never starts an eval run.

import { useRepoEvals } from '../../../queries/repo';
import type { RepoEvalCase } from '../../../types/schemas';
import { fmtSeen } from '../prompts/promptVersionGroups';
import { EvalCaseRow } from './EvalCaseRow';

const EMPTY_CASES: readonly RepoEvalCase[] = [];

function byProfile(cases: readonly RepoEvalCase[]): [string, RepoEvalCase[]][] {
  const groups = new Map<string, RepoEvalCase[]>();
  for (const c of cases) {
    const list = groups.get(c.profile) ?? [];
    list.push(c);
    groups.set(c.profile, list);
  }
  return [...groups.entries()];
}

export function EvalsSection() {
  const { data, isLoading, isError } = useRepoEvals();
  const cases = data?.cases ?? EMPTY_CASES;
  const latest = data?.reports?.[0];
  const failing = cases.filter((c) => !c.pass).length;
  const regressions = cases.filter((c) => c.regression).length;

  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <div className="mb-3">
        <p className="text-theme-text text-sm font-medium">Evals</p>
        <p className="text-theme-muted text-[11px]">
          The latest result per profile and scenario from the saved eval reports.
          {cases.length > 0 &&
            ` ${String(cases.length)} cases · ${String(failing)} failing · ${String(regressions)} regressions.`}
          {latest && ` Newest report: ${latest.mode}, ${fmtSeen(latest.finishedAt)}.`}
        </p>
      </div>
      {isLoading && <p className="text-theme-muted text-xs">Loading eval results…</p>}
      {isError && (
        <p className="text-theme-danger-text text-xs">Could not load the eval results.</p>
      )}
      {data?.error && <p className="text-theme-danger-text text-xs">{data.error}</p>}
      {data && !data.error && cases.length === 0 && (
        <p className="text-theme-muted text-xs">
          No eval reports yet. Run <code className="font-mono">make evals</code> (recorded, free) or{' '}
          <code className="font-mono">ITERVOX_EVALS_LIVE=1 make evals-live</code>; reports are saved
          to <code className="font-mono">.itervox/evals/</code>. Evals are never started from the
          dashboard.
        </p>
      )}
      {byProfile(cases).map(([profile, list]) => (
        <div key={profile} className="mt-3 first:mt-0">
          <p className="text-theme-text mb-1 font-mono text-xs font-semibold">{profile}</p>
          <ul className="space-y-2" aria-label={`Eval results for ${profile}`}>
            {list.map((c) => (
              <EvalCaseRow key={`${c.profile}/${c.scenario}`} c={c} />
            ))}
          </ul>
        </div>
      ))}
    </div>
  );
}
