// Repo Analysis → Agent readiness (#200): the repository-level
// `itervox doctor` checks from GET /api/v1/repo/readiness, one row per check
// with its status and, when it is not a pass, the fix doctor prints.

import { useRepoReadiness } from '../../../queries/repo';
import type { RepoReadinessCheck } from '../../../types/schemas';

const STATUS_STYLE: Record<RepoReadinessCheck['status'], string> = {
  pass: 'border-emerald-400/40 bg-emerald-500/15 text-emerald-400',
  warn: 'border-yellow-400/40 bg-yellow-500/15 text-yellow-400',
  fail: 'border-red-400/40 bg-red-500/15 text-red-400',
};

function ReadinessRow({ check }: { check: RepoReadinessCheck }) {
  return (
    <li className="border-theme-line rounded border p-3" data-check-id={check.id}>
      <div className="flex flex-wrap items-center gap-2">
        <span
          className={`inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] font-bold uppercase ${STATUS_STYLE[check.status]}`}
        >
          {check.status}
        </span>
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
    </li>
  );
}

export function ReadinessSection() {
  const { data, isLoading, isError, isFetching, refetch } = useRepoReadiness();
  const checks = data?.checks ?? [];
  const failing = checks.filter((c) => c.status === 'fail').length;
  const warning = checks.filter((c) => c.status === 'warn').length;

  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <p className="text-theme-text text-sm font-medium">Agent readiness</p>
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
          className="border-theme-line text-theme-text rounded border px-3 py-1.5 text-xs font-semibold hover:bg-[var(--bg-soft)] disabled:opacity-50"
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
