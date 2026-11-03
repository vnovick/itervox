// Runtime analytics: which skills and hooks the session logs show loaded,
// and the analytics recommendations (HIGH_COST_LOW_USAGE, HOOK_STORM,
// CONFIGURED_NOT_LOADED, LOADED_NOT_CONFIGURED, …). Repo Analysis → Runtime
// usage (#200; moved from Settings → SkillsCard).

import type { AnalyticsSnapshotData, Recommendation } from '../../../types/schemas';
import { RecommendationRow } from '../capabilities/RecommendationRow';

const MAX_RECS = 12;

const noFix = () => {
  /* analytics recs have no inline fixes today */
};

interface RuntimeAnalyticsProps {
  analytics: AnalyticsSnapshotData | null | undefined;
  recs: readonly Recommendation[];
}

export function RuntimeAnalytics({ analytics, recs }: RuntimeAnalyticsProps) {
  if (!analytics || analytics.HasRuntimeEvidence === false) {
    return (
      <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
        <p className="text-theme-text mb-1 text-sm font-medium">Runtime analytics</p>
        <p className="text-theme-muted text-xs">
          Runtime evidence not yet available — set{' '}
          <code className="font-mono">CLAUDE_CODE_LOG_DIR</code> and run a few sessions to populate.
        </p>
      </div>
    );
  }

  const verifiedCount = (analytics.SkillStats ?? []).filter((s) => s.RuntimeVerified).length;
  const totalSkills = (analytics.SkillStats ?? []).length;
  const hookRuntimeCount = (analytics.HookStats ?? []).filter(
    (h) => (h.RuntimeLoads ?? 0) > 0,
  ).length;

  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <p className="text-theme-text mb-2 text-sm font-medium">
        Runtime analytics{' '}
        <span className="text-theme-muted text-xs">
          (last scan {new Date(analytics.GeneratedAt).toLocaleTimeString()})
        </span>
      </p>

      <div className="text-theme-muted mb-3 grid grid-cols-3 gap-2 text-xs">
        <div>
          <span className="text-theme-text font-bold tabular-nums">{verifiedCount}</span> /{' '}
          {totalSkills} skills runtime-verified
        </div>
        <div>
          <span className="text-theme-text font-bold tabular-nums">{hookRuntimeCount}</span> hooks
          with activity
        </div>
        <div>
          <span className="text-theme-text font-bold tabular-nums">{recs.length}</span>{' '}
          recommendations
        </div>
      </div>

      {recs.length === 0 ? (
        <p className="text-theme-muted text-xs">No analytics recommendations.</p>
      ) : (
        <ul className="space-y-2">
          {recs.slice(0, MAX_RECS).map((r, idx) => (
            <RecommendationRow
              key={`${r.ID}-${(r.Affected ?? []).join(',')}-${String(idx)}`}
              issue={{
                ID: r.ID,
                Severity: r.Severity,
                Title: r.Title,
                Description: r.Description,
                Affected: r.Affected,
              }}
              onApplyFix={noFix}
              fixPending={false}
            />
          ))}
        </ul>
      )}
    </div>
  );
}
