import { fieldLabelCls, helperTextCls, inputCls } from '../formStyles';
import { CronPicker } from './CronPicker';
import { getIANATimezones } from './timezones';

// The cron trigger's schedule fields (cron expression + IANA timezone). Split
// out of AutomationEditorFields to keep it under the size-budget cap (M6-W2).
// Pure presentational; state lives in the parent form.
export function CronScheduleFields({
  cron,
  timezone,
  onCronChange,
  onTimezoneChange,
}: {
  cron: string;
  timezone: string;
  onCronChange: (value: string) => void;
  onTimezoneChange: (value: string) => void;
}) {
  return (
    <div className="grid gap-4 md:grid-cols-2">
      <div>
        <p id="automation-cron-label" className={fieldLabelCls}>
          Cron
        </p>
        <CronPicker value={cron} onChange={onCronChange} labelledBy="automation-cron-label" />
      </div>

      <div>
        <label htmlFor="automation-timezone-input" className={fieldLabelCls}>
          Timezone
        </label>
        <input
          id="automation-timezone-input"
          list="automation-timezone-zones"
          value={timezone}
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
  );
}
