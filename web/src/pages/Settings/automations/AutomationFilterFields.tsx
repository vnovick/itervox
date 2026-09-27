import { TagInput } from '../../../components/itervox/TagInput';
import { fieldLabelCls, fieldSurfaceCls, helperTextCls, inputCls, selectCls } from '../formStyles';
import type { AutomationFormValues } from './automationForm';
import { InputRequiredFilterFields } from './InputRequiredFilterFields';

// AutomationFilterFields renders the bottom-half filter grid of the automation
// editor: match-mode, states, labels, identifier regex, optional batch limit,
// and optional input-context regex. Extracted from AutomationEditorFields to
// keep that file under the size-budget cap (T-57). Pure presentational; all
// state lives in the parent's RHF form.
export function AutomationFilterFields({
  values,
  availableStates,
  availableLabels,
  supportsBatchLimit,
  isInputRequired,
  onMatchModeChange,
  onStatesChange,
  onLabelsAnyChange,
  onIdentifierRegexChange,
  onLimitChange,
  onInputContextRegexChange,
  onMaxAgeMinutesChange,
}: {
  values: AutomationFormValues;
  availableStates: string[];
  availableLabels: string[];
  supportsBatchLimit: boolean;
  isInputRequired: boolean;
  onMatchModeChange: (value: AutomationFormValues['matchMode']) => void;
  onStatesChange: (value: string[]) => void;
  onLabelsAnyChange: (value: string[]) => void;
  onIdentifierRegexChange: (value: string) => void;
  onLimitChange: (value: string) => void;
  onInputContextRegexChange: (value: string) => void;
  onMaxAgeMinutesChange: (value: string) => void;
}) {
  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <div className={fieldSurfaceCls}>
        <div>
          <label htmlFor="automation-match-mode" className={fieldLabelCls}>
            How to combine multiple filters
          </label>
          <select
            id="automation-match-mode"
            value={values.matchMode}
            disabled={values.unknownFields?.matchMode !== undefined}
            onChange={(event) => {
              onMatchModeChange(event.target.value as AutomationFormValues['matchMode']);
            }}
            className={selectCls}
          >
            <option value="all">Match all populated filters</option>
            <option value="any">Match any populated filter</option>
          </select>
          <p className={helperTextCls}>
            <span className="font-medium">All</span> is stricter and is usually what you want.
            <span className="mt-1 block">
              <span className="font-medium">Any</span> is useful for broad watch rules, such as
              “issues in these states or with these labels”.
            </span>
          </p>
        </div>

        <div>
          <label htmlFor="automation-filter-states" className={fieldLabelCls}>
            States
          </label>
          <TagInput
            id="automation-filter-states"
            chips={values.states}
            onChange={onStatesChange}
            placeholder="+ Add state"
            suggestions={availableStates}
            suggestionLabel="Suggested states from tracker config and currently visible issues"
            chipClassName="bg-[var(--accent-soft)] text-[var(--accent-strong)]"
            addButtonClassName="bg-[var(--accent-soft)] text-[var(--accent-strong)] hover:opacity-80"
          />
          <p className={helperTextCls}>
            For cron automations, leave empty to let Itervox use backlog and active states.
            <span className="mt-1 block">
              For event-based automations, this acts as an extra issue-state guard after the trigger
              fires.
            </span>
          </p>
        </div>

        <div>
          <label htmlFor="automation-filter-labels" className={fieldLabelCls}>
            Labels Any
          </label>
          <TagInput
            id="automation-filter-labels"
            chips={values.labelsAny}
            onChange={onLabelsAnyChange}
            placeholder="+ Add label"
            suggestions={availableLabels}
            suggestionLabel="Suggestions come from issues currently visible to Itervox; you can still type any tracker label."
            chipClassName="bg-[var(--bg-soft)] text-[var(--text-secondary)]"
            addButtonClassName="bg-[var(--bg-soft)] text-[var(--text-secondary)] hover:opacity-80"
          />
          <p className={helperTextCls}>
            Match issues that have at least one of these tracker labels.
          </p>
        </div>
      </div>

      <div className={fieldSurfaceCls}>
        <div>
          <p className="text-theme-text text-[11px] font-semibold">Filter guide</p>
          <p className="text-theme-muted mt-0.5 text-[11px]">
            Filters are optional. Use them to narrow an automation to the exact issues and contexts
            you trust it to handle.
          </p>
        </div>

        <div>
          <label htmlFor="automation-identifier-regex" className={fieldLabelCls}>
            Identifier Regex
          </label>
          <input
            id="automation-identifier-regex"
            value={values.identifierRegex}
            onChange={(event) => {
              onIdentifierRegexChange(event.target.value);
            }}
            placeholder="^ENG-"
            className={`${inputCls} font-mono text-xs`}
          />
          <p className={helperTextCls}>
            Apply a regular expression to issue identifiers like ENG-42.
          </p>
        </div>

        {supportsBatchLimit && (
          <div>
            <label htmlFor="automation-filter-limit" className={fieldLabelCls}>
              Limit
            </label>
            <input
              id="automation-filter-limit"
              value={values.limit}
              onChange={(event) => {
                onLimitChange(event.target.value);
              }}
              inputMode="numeric"
              placeholder="Blank = no limit"
              className={inputCls}
            />
            <p className={helperTextCls}>
              Maximum number of matching issues to queue when one poll or cron tick finds several
              candidates at once.
            </p>
          </div>
        )}

        {isInputRequired && (
          <InputRequiredFilterFields
            values={values}
            onInputContextRegexChange={onInputContextRegexChange}
            onMaxAgeMinutesChange={onMaxAgeMinutesChange}
          />
        )}

        <div className="border-theme-line bg-theme-bg-soft text-theme-text-secondary rounded-[var(--radius-sm)] border px-3 py-3 text-[11px]">
          <p className="text-theme-text font-medium">Why states and labels use suggestions</p>
          <p className="mt-1">
            States come from Itervox tracker settings plus issue states it is already seeing, so the
            state selectors stay aligned with your actual workflow.
          </p>
          <p className="mt-1">
            Labels are different: Itervox does not fetch a tracker-wide label catalogue today, so
            label suggestions are built from issues currently visible to the daemon. Free-form entry
            is still supported when the label you want is not in the suggestion list.
          </p>
        </div>
      </div>
    </div>
  );
}
