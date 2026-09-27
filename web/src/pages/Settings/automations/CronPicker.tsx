import { Suspense, lazy, useId, useRef, useState } from 'react';
import { inputCls, helperTextCls } from '../formStyles';
import './CronPicker.css';

// CORE-098 — antd, react-js-cron and its stylesheet load only with this chunk.
const LazyCronWidget = lazy(() => import('./CronWidget'));

interface CronPickerProps {
  value: string;
  onChange: (cron: string) => void;
  /**
   * Optional placeholder for the bare-input fallback during lazy load and
   * for the always-visible custom-cron text field.
   */
  placeholder?: string;
  /** id of the visible field label; the picker is exposed as a labelled group. */
  labelledBy?: string;
}

/**
 * Visual cron builder backed by react-js-cron. Renders the picker on top
 * and a raw "Custom expression" text input below — both write to the same
 * state via `onChange`. Operators who know cron syntax can type a raw
 * expression directly; everyone else uses the picker.
 *
 * The picker depends on antd. We wrap it in `<ConfigProvider>` with the
 * dark-theme algorithm + a token map sourced from itervox CSS variables so
 * the antd selects feel native to the dashboard.
 *
 * `getPopupContainer` pins each antd Select dropdown to the picker's
 * surrounding `<div>` so the dropdown inherits the modal's stacking
 * context. Without this the dropdown portals to `document.body` at z-index
 * 1050 and disappears behind the modal (z-99999), which manifests as
 * "fields not clickable".
 *
 * The component is lazy-loaded — antd is heavy and not needed until a user
 * actually opens an automation editor.
 */
export function CronPicker({
  value,
  onChange,
  placeholder = '0 9 * * 1-5',
  labelledBy,
}: CronPickerProps) {
  const rawId = useId();
  const wrapperRef = useRef<HTMLDivElement | null>(null);
  // Local copy of the raw text field so typing doesn't fight the picker's
  // re-renders. Kept in sync with `value` whenever the picker mutates it.
  const [rawText, setRawText] = useState(value);
  if (value !== rawText && document.activeElement?.getAttribute('data-cron-raw') !== 'true') {
    // Adopt picker-driven changes only when the raw field isn't focused.
    // Without this guard, a fast user typing in the raw field would have
    // their input wiped on every picker re-render.
    setRawText(value);
  }

  const fallback = (
    <div className="space-y-1">
      <input
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
        placeholder={placeholder}
        className={`${inputCls} font-mono text-xs`}
        aria-label="Cron expression (text input fallback)"
      />
      <p className={helperTextCls}>Loading visual builder…</p>
    </div>
  );

  return (
    <Suspense fallback={fallback}>
      <div
        ref={wrapperRef}
        role="group"
        aria-labelledby={labelledBy}
        data-testid="cron-picker"
        className="cron-picker-wrapper space-y-2"
      >
        <LazyCronWidget
          value={value}
          setValue={(next: string) => {
            onChange(next);
            setRawText(next);
          }}
        />
        <div>
          <label
            htmlFor={rawId}
            className="text-theme-text-secondary mb-1 block text-[11px] font-medium tracking-wider uppercase"
          >
            Custom expression
          </label>
          <input
            id={rawId}
            data-testid="cron-raw-input"
            data-cron-raw="true"
            value={rawText}
            onChange={(event) => {
              const next = event.target.value;
              setRawText(next);
              onChange(next);
            }}
            placeholder={placeholder}
            spellCheck={false}
            autoComplete="off"
            className={`${inputCls} font-mono text-xs`}
            aria-label="Cron expression (raw text)"
          />
          <p className={helperTextCls}>
            Five-field cron: minute hour day month weekday. Picker and text input stay in sync —
            edit either one.
          </p>
        </div>
      </div>
    </Suspense>
  );
}

export default CronPicker;
