// RetriesCard — operator-facing surface for retry-budget knobs that
// already exist in the Go backend (cfg.Agent.MaxRetries, cfg.Tracker.FailedState,
// cfg.Agent.MaxSwitchesPerIssuePerWindow, cfg.Agent.SwitchWindowHours).
//
// max_retries: integer >= 0; "0" is the orchestrator's "unlimited" sentinel.
// failed_state: dropdown sourced from the tracker's known states; the empty
// option "Pause (do not move)" matches the orchestrator's pause-and-persist
// fallback when no failed_state is configured.
//
// The rate-limit switch cap is delegated to SwitchCapSection (gap §5.5).

import { useEffect, useState } from 'react';
import { SwitchCapSection } from './SwitchCapSection';

interface RetriesCardProps {
  maxRetries: number;
  failedState: string;
  trackerStateOptions: readonly string[];
  // The configured success state ("Done"). Excluded from the dropdown so an
  // operator can't accidentally auto-close exhausted issues by routing them
  // to success-on-failure.
  completionState?: string;
  // Gap E — global per-issue switch cap for rate_limited automations.
  maxSwitchesPerIssuePerWindow: number;
  switchWindowHours: number;
  onSetMaxRetries: (n: number) => Promise<boolean>;
  onSetFailedState: (stateName: string) => Promise<boolean>;
  onSetMaxSwitchesPerIssuePerWindow: (n: number) => Promise<boolean>;
  onSetSwitchWindowHours: (h: number) => Promise<boolean>;
}

const PAUSE_LABEL = 'Pause (do not move)';

export function RetriesCard({
  maxRetries,
  failedState,
  trackerStateOptions,
  completionState,
  maxSwitchesPerIssuePerWindow,
  switchWindowHours,
  onSetMaxRetries,
  onSetFailedState,
  onSetMaxSwitchesPerIssuePerWindow,
  onSetSwitchWindowHours,
}: RetriesCardProps) {
  // Local form state so the user can type freely without each keystroke
  // firing a settings PUT. We commit on blur.
  const [draftMaxRetries, setDraftMaxRetries] = useState(String(maxRetries));
  const [savingRetries, setSavingRetries] = useState(false);
  const [retriesError, setRetriesError] = useState('');

  const [savingState, setSavingState] = useState(false);
  const [stateError, setStateError] = useState('');

  // Server-driven snapshot updates re-sync the input when the operator's
  // change is acknowledged (or another tab mutated it).
  useEffect(() => {
    setDraftMaxRetries(String(maxRetries));
  }, [maxRetries]);

  const commitMaxRetries = async () => {
    const trimmed = draftMaxRetries.trim();
    if (!/^\d+$/.test(trimmed)) {
      setRetriesError('Must be a non-negative integer.');
      setDraftMaxRetries(String(maxRetries));
      return;
    }
    const next = Number.parseInt(trimmed, 10);
    if (next === maxRetries) return;
    setSavingRetries(true);
    setRetriesError('');
    const ok = await onSetMaxRetries(next);
    setSavingRetries(false);
    if (!ok) {
      setRetriesError('Failed to save. Please try again.');
      setDraftMaxRetries(String(maxRetries));
    }
  };

  const commitFailedState = async (next: string) => {
    if (next === failedState) return;
    setSavingState(true);
    setStateError('');
    const ok = await onSetFailedState(next);
    setSavingState(false);
    if (!ok) setStateError('Failed to save. Please try again.');
  };

  return (
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      <div className="border-theme-line bg-theme-panel-strong border-b px-5 py-4">
        <h2 className="text-theme-text text-sm font-semibold">Retries</h2>
      </div>
      <div className="space-y-5 px-5 py-5">
        <div>
          <label htmlFor="retries-max" className="text-theme-text block text-sm font-medium">
            Max retries per issue
          </label>
          <p className="text-theme-text-secondary mt-0.5 text-xs leading-relaxed">
            How many times the daemon retries a failing worker before giving up. Each retry uses an
            exponential backoff. Use <span className="font-mono">0</span> for unlimited retries.
          </p>
          <input
            id="retries-max"
            type="number"
            min={0}
            inputMode="numeric"
            value={draftMaxRetries}
            disabled={savingRetries}
            onChange={(e) => {
              setDraftMaxRetries(e.target.value);
            }}
            onBlur={() => {
              void commitMaxRetries();
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.currentTarget.blur();
              }
            }}
            className="border-theme-line bg-theme-bg-soft text-theme-text mt-2 w-32 rounded-md border px-3 py-1.5 text-sm focus:ring-2 focus:ring-[var(--accent)] focus:outline-none"
          />
          {retriesError && (
            <span role="alert" className="text-theme-danger mt-1 block text-xs">
              {retriesError}
            </span>
          )}
        </div>

        <div>
          <label
            htmlFor="retries-failed-state"
            className="text-theme-text block text-sm font-medium"
          >
            On exhausted retries
          </label>
          <p className="text-theme-text-secondary mt-0.5 text-xs leading-relaxed">
            What happens when an issue burns through its retry budget. The daemon comments the cause
            on the issue automatically; this setting controls the workflow transition.
          </p>
          <select
            id="retries-failed-state"
            value={failedState}
            disabled={savingState}
            onChange={(e) => {
              void commitFailedState(e.target.value);
            }}
            className="border-theme-line bg-theme-bg-soft text-theme-text mt-2 max-w-md rounded-md border px-3 py-1.5 text-sm focus:ring-2 focus:ring-[var(--accent)] focus:outline-none"
          >
            <option value="">{PAUSE_LABEL}</option>
            {trackerStateOptions
              .filter((state) => state !== completionState)
              .map((state) => (
                <option key={state} value={state}>
                  Move to {state}
                </option>
              ))}
          </select>
          {stateError && (
            <span role="alert" className="text-theme-danger mt-1 block text-xs">
              {stateError}
            </span>
          )}
        </div>

        <SwitchCapSection
          maxSwitchesPerIssuePerWindow={maxSwitchesPerIssuePerWindow}
          switchWindowHours={switchWindowHours}
          onSetMaxSwitchesPerIssuePerWindow={onSetMaxSwitchesPerIssuePerWindow}
          onSetSwitchWindowHours={onSetSwitchWindowHours}
        />
      </div>
    </div>
  );
}
