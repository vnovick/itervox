import { useMemo } from 'react';
import { useItervoxStore } from '../../store/itervoxStore';
import { Card } from '../../components/ui/Card/Card';
import { AutomationActivityCard } from './AutomationActivityCard';
import type { AutomationDef, HistoryRow, RunningRow } from '../../types/schemas';

/**
 * Activity sub-tab body. Renders one AutomationActivityCard per configured
 * automation, sorted by last-fire-time descending so the most recently active
 * rule sits at the top. Cards filter the snapshot's running + history rows by
 * automationId locally — no extra fetch required.
 */
export default function AutomationsActivityTab() {
  const snapshot = useItervoxStore((state) => state.snapshot);

  const automations: AutomationDef[] = snapshot?.automations ?? [];
  const running: RunningRow[] = snapshot?.running ?? [];
  const history: HistoryRow[] = snapshot?.history ?? [];

  const sorted = useMemo(() => {
    return [...automations].sort((a, b) => {
      const aLast = lastFireTime(a.id, history, running);
      const bLast = lastFireTime(b.id, history, running);
      return bLast - aLast;
    });
  }, [automations, history, running]);

  if (automations.length === 0) {
    return (
      <Card variant="elevated" className="space-y-2">
        <p className="text-theme-text text-sm font-semibold">No automations configured yet</p>
        <p className="text-theme-muted text-sm leading-relaxed">
          Switch to the Configure tab to add your first cron, input-required, or run-failed rule.
        </p>
      </Card>
    );
  }

  return (
    <div className="space-y-4" data-testid="automations-activity">
      {sorted.map((automation) => (
        <AutomationActivityCard
          key={automation.id}
          automation={automation}
          running={running}
          history={history}
        />
      ))}
    </div>
  );
}

function lastFireTime(automationId: string, history: HistoryRow[], running: RunningRow[]): number {
  let best = 0;
  for (const row of running) {
    if (row.automationId === automationId) {
      const t = Date.parse(row.startedAt);
      if (!Number.isNaN(t) && t > best) best = t;
    }
  }
  for (const row of history) {
    if (row.automationId === automationId) {
      const t = Date.parse(row.finishedAt);
      if (!Number.isNaN(t) && t > best) best = t;
    }
  }
  return best;
}
