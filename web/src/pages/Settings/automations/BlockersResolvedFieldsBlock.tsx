import { fieldLabelCls, helperTextCls, inputCls } from '../formStyles';
import type { AutomationFormValues } from './automationForm';

interface BlockersResolvedFieldsBlockProps {
  values: AutomationFormValues;
  availableStates: string[];
  onMoveToStateChange: (value: string) => void;
}

export function BlockersResolvedFieldsBlock({
  values,
  availableStates,
  onMoveToStateChange,
}: BlockersResolvedFieldsBlockProps) {
  return (
    <div className="border-theme-line bg-theme-bg-soft rounded-[var(--radius-sm)] border px-3 py-3">
      <label htmlFor="automation-move-to-state" className={fieldLabelCls}>
        Move to state
      </label>
      <input
        id="automation-move-to-state"
        list="automation-move-to-state-options"
        value={values.moveToState}
        onChange={(event) => {
          onMoveToStateChange(event.target.value);
        }}
        placeholder="Todo"
        className={inputCls}
        autoComplete="off"
        spellCheck={false}
      />
      <datalist id="automation-move-to-state-options">
        {availableStates.map((state) => (
          <option key={state} value={state} />
        ))}
      </datalist>
      <p className={helperTextCls}>
        Optional. Requires the selected profile to allow move_state; leave blank for a comment-only
        readiness run.
      </p>
    </div>
  );
}
