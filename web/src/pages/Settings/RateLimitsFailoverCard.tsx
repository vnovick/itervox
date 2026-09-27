import { Link } from 'react-router';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../store/itervoxStore';
import { EMPTY_AUTOMATIONS } from '../../utils/constants';
import type { AutoSwitchRow, BackendHealthRow } from '../../types/schemas';
import { SwitchCapSection } from './SwitchCapSection';
import { backendHealthChipModels } from '../Dashboard/components/backendHealthChipModel';
import { ClearBreakerButton } from '../Dashboard/components/BackendHealthChips';

// CORE-093 — one home for what happens when an agent backend is rate limited:
//   - the per-issue switch cap (moved here from RetriesCard; setters are the
//     same useSettingsActions calls, which refreshSnapshot() afterwards);
//   - the agent backend breakers (CORE-053) with the operator Clear override
//     (the same mutation as the dashboard chip);
//   - agent.backend_fallback, which is load-time config (not runtime-editable,
//     not in the snapshot), so it is described with a pointer to WORKFLOW.md;
//   - rate_limited automations, linked to their editor on /automations rather
//     than moved: the rule editor needs the full automation form.

const EMPTY_HEALTH: readonly BackendHealthRow[] = [];
const EMPTY_SWITCHES: readonly AutoSwitchRow[] = [];

interface RateLimitsFailoverCardProps {
  maxSwitchesPerIssuePerWindow: number;
  switchWindowHours: number;
  onSetMaxSwitchesPerIssuePerWindow: (n: number) => Promise<boolean>;
  onSetSwitchWindowHours: (h: number) => Promise<boolean>;
}

function titleCase(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export function RateLimitsFailoverCard(props: RateLimitsFailoverCardProps) {
  const { health, automations, autoSwitches } = useItervoxStore(
    useShallow((s) => ({
      health: s.snapshot?.backendHealth ?? EMPTY_HEALTH,
      automations: s.snapshot?.automations ?? EMPTY_AUTOMATIONS,
      autoSwitches: s.snapshot?.autoSwitches ?? EMPTY_SWITCHES,
    })),
  );
  const limitedModels = new Map(backendHealthChipModels(health).map((m) => [m.key, m] as const));
  const rateLimitedRules = automations.filter((a) => a.trigger.type === 'rate_limited').length;

  return (
    <div className="border-theme-line bg-theme-panel space-y-5 rounded-lg border p-4">
      <div>
        <p className="text-theme-text text-sm font-medium">Agent backends</p>
        <p className="text-theme-muted mt-0.5 text-xs">
          A backend that reports a rate limit is paused until its reset; issues wait or move to the
          fallback. Clear a breaker when you know the backend is available again.
        </p>
        {health.length === 0 ? (
          <p className="text-theme-muted mt-2 text-xs italic">
            No agent backend has been rate limited this session.
          </p>
        ) : (
          <ul aria-label="Agent backends" className="mt-2 space-y-1.5">
            {health.map((row) => {
              const key = `${row.backend}@${row.host ?? ''}`;
              const model = limitedModels.get(key);
              return (
                <li key={key} className="flex flex-wrap items-center gap-2 text-xs">
                  <span
                    className={
                      model
                        ? model.danger
                          ? 'text-theme-danger-text'
                          : 'text-theme-warning-text'
                        : 'text-theme-text-secondary'
                    }
                    title={model?.title}
                  >
                    {model
                      ? model.label
                      : `${titleCase(row.backend)}${row.host ? `@${row.host}` : ''} healthy`}
                  </span>
                  {model?.clearable && (
                    <ClearBreakerButton
                      name={model.name}
                      backend={model.backend}
                      host={model.host}
                    />
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>

      <div className="border-theme-line border-t pt-4">
        <p className="text-theme-text text-sm font-medium">Backend fallback</p>
        <p className="text-theme-muted mt-0.5 text-xs">
          Set <code className="font-mono">agent.backend_fallback</code> in{' '}
          <code className="font-mono">WORKFLOW.md</code> to name the backend an issue moves to while
          its own is limited. It is read when the file loads, so edit the file to change it.
          {autoSwitches.length > 0 &&
            ` ${String(autoSwitches.length)} issue${autoSwitches.length === 1 ? '' : 's'} currently run${autoSwitches.length === 1 ? 's' : ''} on an automatic override.`}
        </p>
      </div>

      <div className="border-theme-line border-t pt-4">
        <p className="text-theme-text text-sm font-medium">Rate-limit automations</p>
        <p className="text-theme-muted mt-0.5 text-xs">
          <code className="font-mono">rate_limited</code> automations switch an issue to another
          profile when its backend is limited.{' '}
          <Link to="/automations" className="text-theme-accent-text underline underline-offset-2">
            {rateLimitedRules} rate_limited automation{rateLimitedRules === 1 ? '' : 's'}
          </Link>{' '}
          configured on the Automations page.
        </p>
      </div>

      <div className="border-theme-line border-t pt-4">
        <SwitchCapSection {...props} />
      </div>
    </div>
  );
}
