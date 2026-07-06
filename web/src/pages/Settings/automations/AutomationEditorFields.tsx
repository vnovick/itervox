import { MarkdownPromptEditor } from '../profiles/MarkdownPromptEditor';
import { checkboxCls, fieldLabelCls, helperTextCls, inputCls, selectCls } from '../formStyles';
import type { AutomationFormValues } from './automationForm';
import {
  TRIGGER_OPTIONS,
  triggerDescription,
  type AutomationTriggerType,
} from './automationEditorConstants';
import { AutomationFilterFields } from './AutomationFilterFields';
import { AutomationInstructionsPanel } from './AutomationInstructionsPanel';
import { CronPicker } from './CronPicker';
import { getIANATimezones } from './timezones';
import { RateLimitedFieldsBlock } from './RateLimitedFieldsBlock';
import { BlockersResolvedFieldsBlock } from './BlockersResolvedFieldsBlock';

// Composes extracted automation editor sections while the parent owns form state.
export function AutomationEditorFields({
  values,
  availableProfiles,
  availableStates,
  availableLabels,
  onEnabledChange,
  onProfileChange,
  onInstructionsChange,
  onTriggerTypeChange,
  onTriggerStateChange,
  onCronChange,
  onTimezoneChange,
  onMatchModeChange,
  onStatesChange,
  onLabelsAnyChange,
  onIdentifierRegexChange,
  onLimitChange,
  onInputContextRegexChange,
  onMaxAgeMinutesChange,
  onAutoResumeChange,
  onSwitchToProfileChange,
  onSwitchToBackendChange,
  onCooldownMinutesChange,
  onMoveToStateChange,
}: {
  values: AutomationFormValues;
  availableProfiles: string[];
  availableStates: string[];
  availableLabels: string[];
  onEnabledChange: (value: boolean) => void;
  onProfileChange: (value: string) => void;
  onInstructionsChange: (value: string) => void;
  onTriggerTypeChange: (value: AutomationTriggerType) => void;
  onTriggerStateChange: (value: string) => void;
  onCronChange: (value: string) => void;
  onTimezoneChange: (value: string) => void;
  onMatchModeChange: (value: AutomationFormValues['matchMode']) => void;
  onStatesChange: (value: string[]) => void;
  onLabelsAnyChange: (value: string[]) => void;
  onIdentifierRegexChange: (value: string) => void;
  onLimitChange: (value: string) => void;
  onInputContextRegexChange: (value: string) => void;
  onMaxAgeMinutesChange: (value: string) => void;
  onAutoResumeChange: (value: boolean) => void;
  onSwitchToProfileChange: (value: string) => void;
  onSwitchToBackendChange: (value: '' | 'claude' | 'codex') => void;
  onCooldownMinutesChange: (value: string) => void;
  onMoveToStateChange: (value: string) => void;
}) {
  const isCron = values.triggerType === 'cron';
  const isInputRequired = values.triggerType === 'input_required';
  const isIssueEnteredState = values.triggerType === 'issue_entered_state';
  const isRateLimited = values.triggerType === 'rate_limited';
  const isBlockersResolved = values.triggerType === 'blockers_resolved';
  const supportsBatchLimit =
    values.triggerType === 'cron' ||
    values.triggerType === 'tracker_comment_added' ||
    values.triggerType === 'issue_entered_state' ||
    values.triggerType === 'issue_moved_to_backlog';

  return (
    <div className="space-y-4">
      <label className="border-theme-line bg-theme-bg-soft flex items-start gap-3 rounded-[var(--radius-sm)] border px-3 py-3">
        <input
          type="checkbox"
          checked={values.enabled}
          onChange={(event) => {
            onEnabledChange(event.target.checked);
          }}
          className={checkboxCls}
        />
        <span className="min-w-0">
          <span className="text-theme-text block text-sm font-medium">Enabled</span>
          <span className="text-theme-text-secondary block text-xs">
            Disabled automations stay in configuration but do not dispatch work.
          </span>
        </span>
      </label>

      <div className="grid gap-4 md:grid-cols-2">
        <div>
          <label className={fieldLabelCls}>Profile</label>
          <select
            value={values.profile}
            onChange={(event) => {
              onProfileChange(event.target.value);
            }}
            className={selectCls}
          >
            <option value="">Select profile</option>
            {availableProfiles.map((profile) => (
              <option key={profile} value={profile}>
                {profile}
              </option>
            ))}
          </select>
          <p className={helperTextCls}>
            The selected profile provides the base prompt, backend, and daemon actions.
          </p>
        </div>

        <div>
          <label className={fieldLabelCls}>Trigger</label>
          <select
            value={values.triggerType}
            onChange={(event) => {
              onTriggerTypeChange(event.target.value as AutomationTriggerType);
            }}
            className={selectCls}
          >
            {TRIGGER_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
          <p className={helperTextCls}>{triggerDescription(values.triggerType)}</p>
        </div>
      </div>

      {isIssueEnteredState && (
        <div>
          <label className={fieldLabelCls}>Entered State</label>
          <select
            aria-label="Entered State"
            value={values.triggerState}
            onChange={(event) => {
              onTriggerStateChange(event.target.value);
            }}
            className={selectCls}
          >
            <option value="">Select state</option>
            {availableStates.map((state) => (
              <option key={state} value={state}>
                {state}
              </option>
            ))}
          </select>
          <p className={helperTextCls}>
            Itervox compares the previous observed issue state with the newly fetched state and
            fires only when the issue enters this state.
          </p>
        </div>
      )}

      {isCron && (
        <div className="grid gap-4 md:grid-cols-2">
          <div>
            <label className={fieldLabelCls}>Cron</label>
            <CronPicker value={values.cron} onChange={onCronChange} />
          </div>

          <div>
            <label htmlFor="automation-timezone-input" className={fieldLabelCls}>
              Timezone
            </label>
            <input
              id="automation-timezone-input"
              list="automation-timezone-zones"
              value={values.timezone}
              onChange={(event) => {
                onTimezoneChange(event.target.value);
              }}
              placeholder="UTC or Asia/Jerusalem"
              className={inputCls}
              autoComplete="off"
              spellCheck={false}
            />
            <datalist id="automation-timezone-zones">
              {getIANATimezones().map((zone) => (
                <option key={zone} value={zone} />
              ))}
            </datalist>
            <p className={helperTextCls}>
              IANA zone name. Start typing to filter; leave blank to use the daemon timezone.
            </p>
          </div>
        </div>
      )}

      {isInputRequired && (
        <label className="border-theme-line bg-theme-bg-soft flex items-start gap-3 rounded-[var(--radius-sm)] border px-3 py-3">
          <input
            type="checkbox"
            checked={values.autoResume}
            onChange={(event) => {
              onAutoResumeChange(event.target.checked);
            }}
            className={checkboxCls}
          />
          <span className="min-w-0">
            <span className="text-theme-text block text-sm font-medium">Allow auto-resume</span>
            <span className="text-theme-text-secondary block text-xs">
              When enabled, the helper automation may call{' '}
              <span className="font-mono">itervox action provide-input</span> and resume the blocked
              run automatically.
            </span>
          </span>
        </label>
      )}

      {isRateLimited && (
        <RateLimitedFieldsBlock
          values={values}
          availableProfiles={availableProfiles}
          onAutoResumeChange={onAutoResumeChange}
          onSwitchToProfileChange={onSwitchToProfileChange}
          onSwitchToBackendChange={onSwitchToBackendChange}
          onCooldownMinutesChange={onCooldownMinutesChange}
        />
      )}

      {isBlockersResolved && (
        <BlockersResolvedFieldsBlock
          values={values}
          availableStates={availableStates}
          onMoveToStateChange={onMoveToStateChange}
        />
      )}

      <AutomationInstructionsPanel
        triggerType={values.triggerType}
        onInstructionsChange={onInstructionsChange}
      />

      <MarkdownPromptEditor
        value={values.instructions}
        onChange={onInstructionsChange}
        label="Instructions"
        placeholder="Write small automation-specific instructions in Markdown. These are layered on top of the selected profile."
        helperText={
          <>
            Automation instructions are rendered with Liquid before each run. Use{' '}
            <span className="font-mono">{'{{ issue.* }}'}</span> and{' '}
            <span className="font-mono">{'{{ trigger.* }}'}</span> for runtime context.
          </>
        }
      />

      <AutomationFilterFields
        values={values}
        availableStates={availableStates}
        availableLabels={availableLabels}
        supportsBatchLimit={supportsBatchLimit}
        isInputRequired={isInputRequired}
        onMatchModeChange={onMatchModeChange}
        onStatesChange={onStatesChange}
        onLabelsAnyChange={onLabelsAnyChange}
        onIdentifierRegexChange={onIdentifierRegexChange}
        onLimitChange={onLimitChange}
        onInputContextRegexChange={onInputContextRegexChange}
        onMaxAgeMinutesChange={onMaxAgeMinutesChange}
      />
    </div>
  );
}
