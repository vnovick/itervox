import { fieldLabelCls, helperTextCls, inputCls } from '../formStyles';
import type { AutomationFormValues } from './automationForm';

// The input_required-only filters of the automation editor (input-context
// regex, max age). Split out of AutomationFilterFields to keep it under the
// size-budget cap (M6-W2). Pure presentational; state lives in the parent form.
export function InputRequiredFilterFields({
  values,
  onInputContextRegexChange,
  onMaxAgeMinutesChange,
}: {
  values: AutomationFormValues;
  onInputContextRegexChange: (value: string) => void;
  onMaxAgeMinutesChange: (value: string) => void;
}) {
  return (
    <>
      <div>
        <label htmlFor="automation-input-context-regex" className={fieldLabelCls}>
          Input Context Regex
        </label>
        <input
          id="automation-input-context-regex"
          value={values.inputContextRegex}
          onChange={(event) => {
            onInputContextRegexChange(event.target.value);
          }}
          placeholder="continue|branch"
          className={`${inputCls} font-mono text-xs`}
        />
        <p className={helperTextCls}>
          Match the blocked-agent question text before dispatching the helper profile.
        </p>
      </div>

      <div>
        <label htmlFor="automation-max-age-minutes" className={fieldLabelCls}>
          Max age (minutes)
        </label>
        <input
          id="automation-max-age-minutes"
          data-testid="automation-max-age-minutes"
          value={values.maxAgeMinutes}
          onChange={(event) => {
            onMaxAgeMinutesChange(event.target.value);
          }}
          inputMode="numeric"
          placeholder="Blank = no age limit"
          className={inputCls}
        />
        <p className={helperTextCls}>
          Skip input-required entries that have been queued longer than this many minutes (gap A).
          Stale entries are also flagged on the dashboard so an operator sees what has been
          abandoned.
        </p>
      </div>
    </>
  );
}
