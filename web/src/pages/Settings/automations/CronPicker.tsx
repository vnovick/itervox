import { Suspense, lazy, useRef, useState } from 'react';
import { ConfigProvider, theme as antdTheme } from 'antd';
import { inputCls, helperTextCls } from '../formStyles';

// react-js-cron ships its own stylesheet (Tailwind-friendly, antd-based).
// Load eagerly so the picker is ready before the lazy chunk resolves.
import 'react-js-cron/dist/styles.css';
import './CronPicker.css';

const LazyCron = lazy(async () => {
  const mod = await import('react-js-cron');
  return { default: mod.Cron };
});

interface CronPickerProps {
  value: string;
  onChange: (cron: string) => void;
  /**
   * Optional placeholder for the bare-input fallback during lazy load and
   * for the always-visible custom-cron text field.
   */
  placeholder?: string;
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
export function CronPicker({ value, onChange, placeholder = '0 9 * * 1-5' }: CronPickerProps) {
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

  // Pull theme tokens from CSS variables so antd's components track the
  // dashboard's light/dark mode automatically.
  const cssVar = (name: string, fallbackValue: string) => {
    if (typeof window === 'undefined') return fallbackValue;
    const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || fallbackValue;
  };

  return (
    <Suspense fallback={fallback}>
      <ConfigProvider
        // Mount every antd portal-component (Select dropdowns, Tooltip,
        // Popover) inside the picker wrapper so it inherits the modal's
        // stacking context. The wrapper ref is captured below.
        getPopupContainer={(node) => node?.parentElement ?? document.body}
        theme={{
          algorithm: antdTheme.darkAlgorithm,
          token: {
            colorPrimary: cssVar('--accent', '#6366f1'),
            colorBgContainer: cssVar('--bg-soft', '#1a1f2e'),
            colorBgElevated: cssVar('--bg-elevated', '#11151c'),
            colorBorder: cssVar('--line', '#2a2f3a'),
            colorText: cssVar('--text', '#e5e7eb'),
            colorTextSecondary: cssVar('--text-secondary', '#9ca3af'),
            borderRadius: 6,
            fontSize: 13,
            zIndexPopupBase: 100000, // sit above the editor modal (z-99999)
          },
        }}
      >
        <div ref={wrapperRef} data-testid="cron-picker" className="cron-picker-wrapper space-y-2">
          <LazyCron
            value={value}
            setValue={(next: string) => {
              onChange(next);
              setRawText(next);
            }}
            clearButton={false}
            // humanizeLabels: render "Monday" etc. in the picker UI for ops.
            // humanizeValue=false (explicit) — we MUST emit numeric (1-5)
            // cron tokens because the Go-side `internal/schedule/cron.go`
            // parser only accepts integer day-of-week / month tokens.
            // react-js-cron defaults humanizeValue to true, so an explicit
            // `false` is required (omitting the prop is NOT the same).
            humanizeLabels
            humanizeValue={false}
          />
          <div>
            <label className="text-theme-text-secondary mb-1 block text-[11px] font-medium tracking-wider uppercase">
              Custom expression
            </label>
            <input
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
      </ConfigProvider>
    </Suspense>
  );
}

export default CronPicker;
