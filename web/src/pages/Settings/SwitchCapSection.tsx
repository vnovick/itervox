// Gap §5.5 — extracted from RetriesCard.tsx so each card section owns its
// own draft state + commit handler.
//
// Renders the per-issue rate-limited switch cap controls (gap E):
//   - "switches per N hours" — integer cap (0 = unlimited) and integer hours
//   - commits on blur (typing alone doesn't fire the setter)
//
// Gap §3.4 — both inputs use the shared `useDebouncedCommit` hook, replacing
// the four near-identical commit-on-blur handlers from the original card.

import { useDebouncedCommit } from '../../hooks/useDebouncedCommit';

interface SwitchCapSectionProps {
  maxSwitchesPerIssuePerWindow: number;
  switchWindowHours: number;
  onSetMaxSwitchesPerIssuePerWindow: (n: number) => Promise<boolean>;
  onSetSwitchWindowHours: (h: number) => Promise<boolean>;
}

export function SwitchCapSection({
  maxSwitchesPerIssuePerWindow,
  switchWindowHours,
  onSetMaxSwitchesPerIssuePerWindow,
  onSetSwitchWindowHours,
}: SwitchCapSectionProps) {
  const switches = useDebouncedCommit({
    value: maxSwitchesPerIssuePerWindow,
    setter: onSetMaxSwitchesPerIssuePerWindow,
    parse: (draft) => {
      const trimmed = draft.trim();
      if (!/^\d+$/.test(trimmed)) {
        return { ok: false, error: 'Switch cap must be a non-negative integer.' };
      }
      return { ok: true, value: Number.parseInt(trimmed, 10) };
    },
  });

  // Note: variable named `windowH` (not `window`) to avoid shadowing the
  // browser global `window` — would break e.g. `getComputedStyle` in tests.
  const windowH = useDebouncedCommit({
    value: switchWindowHours,
    setter: onSetSwitchWindowHours,
    parse: (draft) => {
      const trimmed = draft.trim();
      if (!/^\d+$/.test(trimmed) || Number.parseInt(trimmed, 10) <= 0) {
        return { ok: false, error: 'Window hours must be a positive integer.' };
      }
      return { ok: true, value: Number.parseInt(trimmed, 10) };
    },
  });

  // The two sub-fields share a row; surface either error in a single alert
  // beneath both inputs (matches the prior visual contract).
  const error = switches.error || windowH.error;
  const saving = switches.saving || windowH.saving;

  return (
    <div>
      <label htmlFor="retries-max-switches" className="text-theme-text block text-sm font-medium">
        Rate-limit switch cap
      </label>
      <p className="text-theme-text-secondary mt-0.5 text-xs leading-relaxed">
        How many times a <span className="font-mono">rate_limited</span> automation can swap an
        issue to a different profile within the rolling window. Use{' '}
        <span className="font-mono">0</span> for unlimited.
      </p>
      <div className="mt-2 flex items-center gap-3">
        <input
          id="retries-max-switches"
          type="number"
          min={0}
          inputMode="numeric"
          value={switches.draft}
          disabled={saving}
          onChange={(e) => {
            switches.setDraft(e.target.value);
          }}
          onBlur={() => {
            void switches.commit();
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.currentTarget.blur();
            }
          }}
          className="border-theme-line bg-theme-bg-soft text-theme-text w-32 rounded-md border px-3 py-1.5 text-sm focus:ring-2 focus:ring-[var(--accent)] focus:outline-none"
        />
        <span className="text-theme-text-secondary text-xs">switches per</span>
        <input
          id="retries-switch-window"
          type="number"
          min={1}
          inputMode="numeric"
          value={windowH.draft}
          disabled={saving}
          onChange={(e) => {
            windowH.setDraft(e.target.value);
          }}
          onBlur={() => {
            void windowH.commit();
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.currentTarget.blur();
            }
          }}
          className="border-theme-line bg-theme-bg-soft text-theme-text w-24 rounded-md border px-3 py-1.5 text-sm focus:ring-2 focus:ring-[var(--accent)] focus:outline-none"
        />
        <span className="text-theme-text-secondary text-xs">hours</span>
      </div>
      {error && (
        <span role="alert" className="text-theme-danger mt-1 block text-xs">
          {error}
        </span>
      )}
    </div>
  );
}
