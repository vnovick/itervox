import { unknownValueLabel } from '../../types/configRoundTrip';

/**
 * CORE-047 round 2 — read-only notice for config values this dashboard does
 * not know (sent by a newer daemon). They are shown, never edited, and saved
 * back unchanged.
 */
export function UnknownValueNotice({ items }: { items: readonly { what: string; raw: string }[] }) {
  if (items.length === 0) return null;
  return (
    <div
      role="note"
      data-testid="unknown-value-notice"
      className="border-theme-warning bg-theme-warning-soft text-theme-warning-text rounded-[var(--radius-sm)] border px-3 py-2 text-xs"
    >
      <ul className="list-disc pl-4">
        {items.map((item) => (
          <li key={`${item.what}:${item.raw}`}>{unknownValueLabel(item.what, item.raw)}</li>
        ))}
      </ul>
      <p className="mt-1">
        These are read-only here and are saved back unchanged. Upgrade the dashboard (reload the
        page) or edit WORKFLOW.md to change them.
      </p>
    </div>
  );
}
