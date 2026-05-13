// Gap E — the conditional block of fields that appears in
// AutomationEditorFields when triggerType === 'rate_limited'. Extracted to a
// separate component so AutomationEditorFields stays within its 280-line
// size-budget cap and the rate_limited UX can grow independently (cooldown
// presets, per-rule cap overrides, etc.).

import { checkboxCls, fieldLabelCls, helperTextCls, inputCls, selectCls } from '../formStyles';
import type { AutomationFormValues } from './automationForm';

interface RateLimitedFieldsBlockProps {
  values: AutomationFormValues;
  availableProfiles: string[];
  onAutoResumeChange: (value: boolean) => void;
  onSwitchToProfileChange: (value: string) => void;
  onSwitchToBackendChange: (value: '' | 'claude' | 'codex') => void;
  onCooldownMinutesChange: (value: string) => void;
}

export function RateLimitedFieldsBlock({
  values,
  availableProfiles,
  onAutoResumeChange,
  onSwitchToProfileChange,
  onSwitchToBackendChange,
  onCooldownMinutesChange,
}: RateLimitedFieldsBlockProps) {
  return (
    <div className="border-theme-line bg-theme-bg-soft space-y-3 rounded-[var(--radius-sm)] border px-3 py-3">
      <div>
        <label htmlFor="automation-switch-to-profile" className={fieldLabelCls}>
          Switch to profile
        </label>
        <select
          id="automation-switch-to-profile"
          value={values.switchToProfile}
          onChange={(event) => {
            onSwitchToProfileChange(event.target.value);
          }}
          className={selectCls}
        >
          <option value="">— select fallback profile —</option>
          {availableProfiles.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
        <p className={helperTextCls}>
          When this rule fires, the issue is re-dispatched under this profile. Required.
        </p>
        {/* Gap §5.4 — warn when a previously-saved profile no longer exists.
            Without this, a deleted profile silently fails at dispatch time
            and the operator has no signal in the editor. */}
        {values.switchToProfile !== '' && !availableProfiles.includes(values.switchToProfile) && (
          <p
            role="alert"
            className="text-theme-danger mt-1 text-xs"
            data-testid="rate-limited-missing-profile-warning"
          >
            Profile <span className="font-mono">{values.switchToProfile}</span> is no longer
            configured. Pick a different profile or recreate this one before saving.
          </p>
        )}
      </div>

      <div>
        <label htmlFor="automation-switch-to-backend" className={fieldLabelCls}>
          Override backend (optional)
        </label>
        <select
          id="automation-switch-to-backend"
          value={values.switchToBackend}
          onChange={(event) => {
            onSwitchToBackendChange(event.target.value as '' | 'claude' | 'codex');
          }}
          className={selectCls}
        >
          <option value="">Use the new profile&rsquo;s default</option>
          <option value="claude">claude</option>
          <option value="codex">codex</option>
        </select>
        <p className={helperTextCls}>
          Power-user. Same prompt, different CLI — prompts tuned for one backend often misbehave on
          the other.
        </p>
      </div>

      <div>
        <label htmlFor="automation-cooldown-minutes" className={fieldLabelCls}>
          Cooldown (minutes)
        </label>
        <input
          id="automation-cooldown-minutes"
          type="text"
          inputMode="numeric"
          value={values.cooldownMinutes}
          onChange={(event) => {
            onCooldownMinutesChange(event.target.value);
          }}
          placeholder="30"
          className={inputCls}
        />
        <p className={helperTextCls}>
          After this rule fires for an (issue, profile) tuple, mute it for N minutes to prevent
          thrash when both backends are throttled. Default 30 when blank.
        </p>
      </div>

      <label className="border-theme-line bg-theme-bg-elevated flex items-start gap-3 rounded-[var(--radius-sm)] border px-3 py-3">
        <input
          type="checkbox"
          checked={values.autoResume}
          onChange={(event) => {
            onAutoResumeChange(event.target.checked);
          }}
          className={checkboxCls}
        />
        <span className="min-w-0">
          <span className="text-theme-text block text-sm font-medium">
            Auto-switch (no human in the loop)
          </span>
          <span className="text-theme-text-secondary block text-xs">
            When enabled, the orchestrator immediately overrides the issue&rsquo;s profile (and
            backend, if set) and re-dispatches. Required for rate-limited switching in v0.2.0.
          </span>
        </span>
      </label>
    </div>
  );
}
