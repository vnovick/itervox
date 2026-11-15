// Repo Analysis → Agent readiness (#200): the repository-level
// `itervox doctor` checks from GET /api/v1/repo/readiness, one row per check
// with its status and, when it is not a pass, the fix doctor prints.

import { RequireScope } from '../../../components/itervox/RequireScope';
import { useCreateVerifySkillIssue, useRepoReadiness } from '../../../queries/repo';
import type { RepoReadinessCheck } from '../../../types/schemas';
import { buttonClass } from '../../../components/ui/button/buttonStyles';

/** #218 — the check the "Generate verify skill" action answers. */
const VERIFY_SKILL_CHECK = 'verify_skill';

/** #218 — creates an issue asking an agent to write the verify skill. */
function GenerateVerifySkill() {
  const create = useCreateVerifySkillIssue();
  return (
    <RequireScope scope="operator">
      <button
        type="button"
        disabled={create.isPending || create.isSuccess}
        onClick={() => {
          create.mutate();
        }}
        className={buttonClass('secondary', 'sm', 'mt-2', true)}
      >
        {create.isPending
          ? 'Creating issue…'
          : create.isSuccess
            ? `Created ${create.data.identifier ?? 'issue'}`
            : 'Generate verify skill'}
      </button>
    </RequireScope>
  );
}

const STATUS_STYLE: Record<RepoReadinessCheck['status'], string> = {
  pass: 'pill-ok',
  warn: 'pill-warn',
  fail: 'pill-danger',
};

function ReadinessRow({ check }: { check: RepoReadinessCheck }) {
  return (
    <li className="glass-card px-3.5 py-3" data-check-id={check.id}>
      <div className="flex flex-wrap items-center gap-2">
        <span className={`pill pill-sm ${STATUS_STYLE[check.status]}`}>{check.status}</span>
        <span className="text-theme-text text-xs font-semibold">{check.title}</span>
        <code className="text-theme-muted ml-auto font-mono text-[10px]">{check.id}</code>
      </div>
      {check.detail && <p className="text-theme-muted mt-1 text-xs">{check.detail}</p>}
      {check.status !== 'pass' && check.fix && (
        <p className="text-theme-text mt-1 text-xs">
          <span className="font-semibold">Fix: </span>
          {check.fix}
        </p>
      )}
      {check.id === VERIFY_SKILL_CHECK && check.status !== 'pass' && <GenerateVerifySkill />}
    </li>
  );
}

export function ReadinessSection() {
  const { data, isLoading, isError, isFetching, refetch } = useRepoReadiness();
  const checks = data?.checks ?? [];
  const failing = checks.filter((c) => c.status === 'fail').length;
  const warning = checks.filter((c) => c.status === 'warn').length;

  return (
    <div className="glass-panel px-5 py-[18px]">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <p className="section-title">Agent readiness</p>
          <p className="text-theme-muted text-[11px]">
            The repository checks <code className="font-mono">itervox doctor</code> runs.
            {data && ` ${String(failing)} failing · ${String(warning)} warnings.`}
          </p>
        </div>
        <button
          type="button"
          onClick={() => {
            void refetch();
          }}
          disabled={isFetching}
          className={buttonClass('secondary', 'sm', undefined, true)}
        >
          {isFetching ? 'Checking…' : 'Re-run checks'}
        </button>
      </div>
      {isLoading && <p className="text-theme-muted text-xs">Running checks…</p>}
      {isError && <p className="text-theme-danger-text text-xs">Could not run the checks.</p>}
      {checks.length > 0 && (
        <ul className="space-y-2" aria-label="Readiness checks">
          {checks.map((c) => (
            <ReadinessRow key={c.id} check={c} />
          ))}
        </ul>
      )}
    </div>
  );
}
