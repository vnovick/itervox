// OpsChip is the shared tile primitive for the dashboard's live-ops strip.
// Extracted from LiveOpsStrip.tsx so sibling chip components (e.g.
// DispatchPressureChip) can render the same tile without importing from
// LiveOpsStrip and forming an import cycle.
export function OpsChip({
  label,
  danger = false,
  warning = false,
  title,
}: {
  label: string;
  danger?: boolean;
  // critical-path-ordering Task 6 — second severity tone alongside `danger`,
  // for conditions that warrant operator attention but are less severe than
  // a hard blocker (e.g. stale-blocked issues vs. dependency cycles).
  warning?: boolean;
  // Optional hover explanation for tiles whose label is a compressed metric
  // the operator cannot interpret from the label alone.
  title?: string;
}) {
  return (
    <span
      title={title}
      className={`inline-flex min-h-9 flex-shrink-0 items-center rounded-[var(--radius-sm)] px-2.5 py-1 text-[12px] font-medium whitespace-nowrap ${
        danger
          ? 'bg-theme-danger-soft text-theme-danger'
          : warning
            ? 'bg-theme-warning-soft text-theme-warning'
            : 'bg-theme-bg-soft text-theme-text-secondary'
      }`}
    >
      {label}
    </span>
  );
}
