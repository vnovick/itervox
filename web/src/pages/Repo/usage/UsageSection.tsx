// Repo Analysis → Runtime usage and cost (#200): the runtime analytics and
// its recommendations, and the per-profile token breakdown.

import { useSkillsAnalytics, useSkillsAnalyticsRecommendations } from '../../../queries/skills';
import type { ProfileCost } from '../../../types/schemas';
import { ProfileCostsTable } from './ProfileCostsTable';
import { RuntimeAnalytics } from './RuntimeAnalytics';

const EMPTY_COSTS: readonly ProfileCost[] = [];

export function UsageSection() {
  const { data: analytics, isLoading } = useSkillsAnalytics();
  const { data: recs = [] } = useSkillsAnalyticsRecommendations();

  if (isLoading) {
    return <p className="text-theme-muted text-sm">Loading runtime analytics…</p>;
  }
  return (
    <div className="space-y-6">
      <RuntimeAnalytics analytics={analytics} recs={recs} />
      <ProfileCostsTable costs={analytics?.ProfileCosts ?? EMPTY_COSTS} />
    </div>
  );
}
