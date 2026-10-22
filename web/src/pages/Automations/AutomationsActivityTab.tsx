import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../store/itervoxStore';
import { EMPTY_AUTOMATIONS, EMPTY_HISTORY, EMPTY_RUNNING } from '../../utils/constants';
import { Card } from '../../components/ui/Card/Card';
import { AutomationActivityCard } from './AutomationActivityCard';
import type { HistoryRow, RunningRow } from '../../types/schemas';

/**
 * Activity sub-tab body. Renders one AutomationActivityCard per configured
 * automation, sorted by last-fire-time descending so the most recently active
 * rule sits at the top. Cards filter the snapshot's running + history rows by
 * automationId locally — no extra fetch required.
 */
export default function AutomationsActivityTab() {
  // #211 — narrow selector: only the three branches this tab reads.
  const { automations, running, history } = useItervoxStore(
    useShallow((state) => ({
      automations: state.snapshot?.automations ?? EMPTY_AUTOMATIONS,
      running: state.snapshot?.running ?? EMPTY_RUNNING,
      history: state.snapshot?.history ?? EMPTY_HISTORY,
    })),
  );

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
