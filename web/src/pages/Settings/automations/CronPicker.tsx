import { useId, useState } from 'react';
import { inputCls, helperTextCls, selectCls } from '../formStyles';
import {
  CRON_FIELDS,
  CRON_PRESETS,
  describeCron,
  joinCronFields,
  parseCron,
  splitCronFields,
  type CronFields,
} from '../../../lib/cron';

interface CronPickerProps {
  value: string;
  onChange: (cron: string) => void;
  /** Placeholder for the raw expression field. */
  placeholder?: string;
  /** id of the visible field label; the picker is exposed as a labelled group. */
  labelledBy?: string;
}

const CUSTOM = 'custom';
const subLabelCls =
  'text-theme-text-secondary mb-1 block text-[11px] font-medium tracking-wider uppercase';

/**
 * #211 — light cron input: a preset menu, five per-field inputs and a raw
 * expression field, all writing the same value through `onChange`, plus a
 * plain-English preview. Validation mirrors the daemon's parser
 * (internal/schedule/cron.go) through `lib/cron`, so what this editor calls
 * valid is exactly what the daemon schedules. It replaces the antd +
 * react-js-cron builder, which cost a ~295 kB chunk.
 */
export function CronPicker({
  value,
  onChange,
  placeholder = '0 9 * * 1-5',
  labelledBy,
}: CronPickerProps) {
  const baseId = useId();
  const presetId = `${baseId}-preset`;
  const rawId = `${baseId}-raw`;
  const previewId = `${baseId}-preview`;

  // Field drafts survive a half-typed (empty) field; any value that did not
  // come from the field inputs themselves (a preset, the raw input, a parent
  // reset) re-derives them.
  const [lastEmitted, setLastEmitted] = useState<string | null>(null);
  const [drafts, setDrafts] = useState<CronFields>(() => splitCronFields(value));
  const [draftsFor, setDraftsFor] = useState(value);
  if (value !== draftsFor) {
    setDraftsFor(value);
    if (value !== lastEmitted) setDrafts(splitCronFields(value));
  }

  const emitFields = (next: CronFields) => {
    setDrafts(next);
    const expr = joinCronFields(next);
    setLastEmitted(expr);
    onChange(expr);
  };

  const parsed = parseCron(value);
  // A field emptied in the field editor names that field; any other invalid
  // value shows the parser's own message.
  const fromFields = value === lastEmitted;
  const emptyIndex = parsed.ok || !fromFields ? -1 : drafts.findIndex((f) => f.trim() === '');
  const error = parsed.ok
    ? null
    : emptyIndex >= 0
      ? `${CRON_FIELDS[emptyIndex].label} is empty.`
      : parsed.error;
  const errorField = parsed.ok
    ? null
    : emptyIndex >= 0
      ? CRON_FIELDS[emptyIndex].name
      : parsed.field;
  const preset = CRON_PRESETS.find((p) => p.expr === value.trim().replace(/\s+/g, ' '));

  return (
    <div
      role="group"
      aria-labelledby={labelledBy}
      data-testid="cron-picker"
      className="border-theme-line bg-theme-bg-elevated space-y-3 rounded-[var(--radius-md)] border p-3"
    >
      <div>
        <label htmlFor={presetId} className={subLabelCls}>
          Preset
        </label>
        <select
          id={presetId}
          data-testid="cron-preset"
          value={preset?.id ?? CUSTOM}
          onChange={(event) => {
            const next = CRON_PRESETS.find((p) => p.id === event.target.value);
            if (next) onChange(next.expr);
          }}
          className={`${selectCls} text-xs`}
        >
          <option value={CUSTOM} disabled>
            Custom
          </option>
          {CRON_PRESETS.map((p) => (
            <option key={p.id} value={p.id}>
              {p.label} ({p.expr})
            </option>
          ))}
        </select>
      </div>

      <div className="grid grid-cols-5 gap-2">
        {CRON_FIELDS.map((spec, i) => {
          const fieldId = `${baseId}-${spec.name}`;
          return (
            <div key={spec.name} className="min-w-0">
              <label htmlFor={fieldId} className={`${subLabelCls} truncate text-[10px]`}>
                {spec.label}
              </label>
              <input
                id={fieldId}
                data-testid={`cron-field-${spec.name}`}
                value={drafts[i]}
                onChange={(event) => {
                  const next: CronFields = [...drafts];
                  next[i] = event.target.value;
                  emitFields(next);
                }}
                aria-invalid={errorField === spec.name}
                aria-describedby={previewId}
                placeholder="*"
                spellCheck={false}
                autoComplete="off"
                className={`${inputCls} px-2 text-center font-mono text-xs`}
              />
              <p className="text-theme-muted mt-0.5 text-center text-[10px]">
                {String(spec.min)}–{String(spec.max)}
              </p>
            </div>
          );
        })}
      </div>

      <div>
        <label htmlFor={rawId} className={subLabelCls}>
          Custom expression
        </label>
        <input
          id={rawId}
          data-testid="cron-raw-input"
          value={value}
          onChange={(event) => {
            onChange(event.target.value);
          }}
          aria-invalid={error !== null}
          aria-describedby={previewId}
          placeholder={placeholder}
          spellCheck={false}
          autoComplete="off"
          className={`${inputCls} font-mono text-xs`}
        />
      </div>

      <p
        id={previewId}
        data-testid={error ? 'cron-error' : 'cron-preview'}
        aria-live="polite"
        className={error ? 'text-theme-danger text-xs' : 'text-theme-text text-xs'}
      >
        {error ? `Invalid cron: ${error}` : describeCron(value)}
      </p>
      <p className={helperTextCls}>
        Five fields: minute hour day month weekday. Each takes *, a number, a range (1-5), a list
        (1,15) or a step (*/15). Weekday 0 is Sunday; names and @aliases are not supported.
      </p>
    </div>
  );
}

export default CronPicker;
