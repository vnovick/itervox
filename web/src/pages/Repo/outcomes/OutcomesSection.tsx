// Repo Analysis → Outcomes (cost and outcome visibility): how the agents'
// runs turned out per profile and per playbook over the last 7 or 30 days.
// Loaded as its own lazy chunk; data from GET /api/v1/outcomes.

import { useState } from 'react';
import { OUTCOME_WINDOWS, useOutcomes, type OutcomeWindow } from '../../../queries/outcomes';
import type { OutcomeGroupRow } from '../../../types/schemas';
import { OutcomeTable } from './OutcomeTable';

const EMPTY_ROWS: readonly OutcomeGroupRow[] = [];
const WINDOW_LABEL: Record<OutcomeWindow, string> = { '7d': '7 days', '30d': '30 days' };

export default function OutcomesSection() {
  const [win, setWin] = useState<OutcomeWindow>('30d');
  const { data, isLoading, isError } = useOutcomes(win);

  return (
    <div className="space-y-4">
      <div role="group" aria-label="Outcomes window" className="segmented inline-flex">
        {OUTCOME_WINDOWS.map((w) => (
          <button
            key={w}
            type="button"
            aria-pressed={w === win}
            onClick={() => {
              setWin(w);
            }}
          >
            {WINDOW_LABEL[w]}
          </button>
        ))}
      </div>
      {isLoading ? (
        <p className="text-theme-muted text-sm">Loading outcomes…</p>
      ) : isError || !data ? (
        <p className="text-theme-danger-text text-sm">Could not load the outcomes.</p>
      ) : (
        <>
          <p className="text-theme-text-secondary text-sm">
            {data.total.runs} run{data.total.runs === 1 ? '' : 's'} in the last {WINDOW_LABEL[win]}
            {data.total.prs > 0
              ? `, ${String(data.total.prsMerged)} of ${String(data.total.prs)} PRs merged`
              : ''}
            .
          </p>
          <OutcomeTable title="Profile" emptyName="(default)" rows={data.profiles ?? EMPTY_ROWS} />
          <OutcomeTable title="Playbook" emptyName="(none)" rows={data.playbooks ?? EMPTY_ROWS} />
          {data.earliestRunAt && new Date(data.earliestRunAt) > new Date(data.windowStart) && (
            <p className="text-theme-muted text-xs">
              Run history is bounded: it starts {new Date(data.earliestRunAt).toLocaleDateString()},
              inside this window.
            </p>
          )}
        </>
      )}
    </div>
  );
}
