import { ConfigProvider, theme as antdTheme } from 'antd';
import { Cron } from 'react-js-cron';
import 'react-js-cron/dist/styles.css';

// CORE-098 — antd (ConfigProvider + theme) and react-js-cron live only in this
// module, which CronPicker imports lazily. Before, CronPicker imported antd
// statically, so the Automations route chunk pulled the ~38 kB-gzip antd
// config-provider chunk on every visit, whether or not a cron editor opened.

/** Pull theme tokens from CSS variables so antd tracks the dashboard theme. */
function cssVar(name: string, fallbackValue: string): string {
  if (typeof window === 'undefined') return fallbackValue;
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallbackValue;
}

export interface CronWidgetProps {
  value: string;
  setValue: (next: string) => void;
}

export default function CronWidget({ value, setValue }: CronWidgetProps) {
  return (
    <ConfigProvider
      // Mount every antd portal-component (Select dropdowns, Tooltip,
      // Popover) inside the picker wrapper so it inherits the modal's
      // stacking context.
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
      <Cron
        value={value}
        setValue={setValue}
        clearButton={false}
        // humanizeLabels: render "Monday" etc. in the picker UI for ops.
        // humanizeValue=false (explicit) — we MUST emit numeric (1-5) cron
        // tokens because the Go-side `internal/schedule/cron.go` parser only
        // accepts integer day-of-week / month tokens. react-js-cron defaults
        // humanizeValue to true, so an explicit `false` is required.
        humanizeLabels
        humanizeValue={false}
      />
    </ConfigProvider>
  );
}
